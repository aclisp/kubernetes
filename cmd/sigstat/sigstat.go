// The kstat is a vmstat like tool for kubelet managed containers
package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/golang/glog"
	cadvisorApi "github.com/google/cadvisor/info/v1"
	//cadvisorApiV2 "github.com/google/cadvisor/info/v2"
	cadvisorHttp "github.com/google/cadvisor/http"
	"github.com/google/cadvisor/cache/memory"
	"github.com/google/cadvisor/manager"
	"github.com/google/cadvisor/utils/sysfs"

	"k8s.io/kubernetes/pkg/kubelet/dockertools"
	"k8s.io/kubernetes/pkg/util"
)

const (
	statsCacheDuration = 2 * time.Minute
	maxHousekeepingInterval = 15 * time.Second
	allowDynamicHousekeeping = true
)

type cadvisorClient struct {
	manager.Manager
}

func newCadvisorClient() (*cadvisorClient, error) {
	sysFs, err := sysfs.NewRealSysFs()
	if err != nil {
		return &cadvisorClient{}, err
	}

	// Create and start the cAdvisor container manager.
	m, err := manager.New(memory.New(statsCacheDuration, nil), sysFs, maxHousekeepingInterval, allowDynamicHousekeeping)
	if err != nil {
		return &cadvisorClient{}, err
	}

	cadvisorClient := &cadvisorClient{
		Manager: m,
	}

	return cadvisorClient, nil
}

func main() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, os.Kill, syscall.SIGHUP, syscall.SIGTERM)
	runtime.GOMAXPROCS(runtime.NumCPU())

	util.InitFlags()
	util.InitLogs()
	defer util.FlushLogs()

	cadvisorClient, err := newCadvisorClient()
	if err != nil {
		glog.Fatal(err)
	}

	err = cadvisorClient.Start()
	if err != nil {
		glog.Fatal(err)
	}
	defer cadvisorClient.Stop()

	err = cadvisorClient.exportHTTP(13394)
	if err != nil {
		glog.Fatal(err)
	}

	count := 0
	buf := NewFlushSyncWriter()
	defer func() {
		buf.Flush()
		buf.Sync()
	}()

	stop := make(chan struct{})
	util.Until(func () {
		select {
		case <-sig:
			close(stop)
		default:
		}
		cadvisorClient.printAllContainers(buf, count)
		count++
	}, 1*time.Second, stop)
}

func (cc *cadvisorClient) printAllContainers(b FlushSyncWriter, count int) {
	machine, err := cc.GetMachineInfo()
	if err != nil {
		glog.Fatal(err)
	}
	rootCont, err := cc.GetContainerInfo("/", &cadvisorApi.ContainerInfoRequest{NumStats:1})
	if err != nil {
		glog.Fatal(err)
	}

	if count % 60 == 0 {
		fmt.Fprintf(b, "/ root container")
		fmt.Fprintf(b, " CPU core %s", format(int64(machine.NumCores * 1000)))
		fmt.Fprintf(b, " MEM max-swa %s %s", format(int64(rootCont.Spec.Memory.Limit)), format(int64(rootCont.Spec.Memory.SwapLimit)))
		fmt.Fprintf(b, "\n")
	}

	containers, err := cc.AllDockerContainers(&cadvisorApi.ContainerInfoRequest{NumStats:2})
	if err != nil {
		glog.Fatal(err)
	}
	var contNames []string
	for contName := range containers {
		contNames = append(contNames, contName)
	}
	sort.Strings(contNames)

	excludes := map[string]bool {
		"POD": true,
		"data-volume": true,
	}

	for _, contName := range contNames {
		cont := containers[contName]
		podFullName := ""
		podContainerName := ""
		if len(cont.Aliases) > 0 {
			kubeletContainerName, _, err := dockertools.ParseDockerName(cont.Aliases[0])
			if err == nil {
				podFullName = kubeletContainerName.PodFullName
				podContainerName = kubeletContainerName.ContainerName
			}
		}

		if excludes[podContainerName] {
			continue
		}

		contSpecCpuLimit := cont.Spec.Cpu.Limit * 1000 / 1024
		contSpecCpuMaxLimit := cont.Spec.Cpu.MaxLimit * 1000 / 100000

		if count % 20 == 0 {
			fmt.Fprintf(b, "%s %s %s", cont.Name[:20], podFullName, podContainerName)
			fmt.Fprintf(b, " CPU req-lim %s %s", format(int64(contSpecCpuLimit)), format(int64(contSpecCpuMaxLimit)))
			fmt.Fprintf(b, " MEM lim-swa %s %s", format(int64(cont.Spec.Memory.Limit)), format(int64(cont.Spec.Memory.SwapLimit - cont.Spec.Memory.Limit)))
			fmt.Fprintf(b, " CPU usr-sys-tol-fai")
			fmt.Fprintf(b, " MEM hot-use-max-fai-swa")
			fmt.Fprintf(b, " IO dev-rMB-wMB")
			fmt.Fprintf(b, "\n")
		}

		for i := range cont.Stats[1:] {
			cur := cont.Stats[i+1]
			prev := cont.Stats[i]
			if !cur.Timestamp.After(prev.Timestamp) {
				glog.Errorf("container stats move backwards in time")
				continue
			}
			timeDelta := cur.Timestamp.Sub(prev.Timestamp)
			if timeDelta <= 100*time.Millisecond {
				glog.Errorf("time delta unexpectedly small")
				continue
			}
			timeDeltaNs := uint64(timeDelta.Nanoseconds())
			convertToRate := func(prevValue, curValue uint64) uint64 {
				valueDelta := uint64(0)
				if curValue > prevValue {
					valueDelta = curValue - prevValue
				}
				return valueDelta * 1000 / timeDeltaNs
			}
			convertToUtil := func(curValue, limitValue uint64) uint64 {
				return curValue * 100 / limitValue
			}

			cpuUserRate := convertToRate(prev.Cpu.Usage.User, cur.Cpu.Usage.User)
			cpuSystemRate := convertToRate(prev.Cpu.Usage.System, cur.Cpu.Usage.System)
			cpuTotalRate := convertToRate(prev.Cpu.Usage.Total, cur.Cpu.Usage.Total)
			fmt.Fprintf(b, "  %s %s | %2d,%2d %2d,%2d %2d,%2d %3d | ",
				cur.Timestamp.Format(time.Stamp),
				podFullName,
				cpuUserRate / 100,
				convertToUtil(cpuUserRate, contSpecCpuMaxLimit),
				cpuSystemRate / 100,
				convertToUtil(cpuSystemRate, contSpecCpuMaxLimit),
				cpuTotalRate / 100,
				convertToUtil(cpuTotalRate, contSpecCpuMaxLimit),
				cur.Cpu.Throttling.ThrottledPeriods)

			fmt.Fprintf(b, "%2d %2d %2d %3d %2d | ",
				convertToUtil(cur.Memory.WorkingSet, cont.Spec.Memory.Limit),
				convertToUtil(cur.Memory.Usage, cont.Spec.Memory.Limit),
				convertToUtil(cur.Memory.MaxUsage, cont.Spec.Memory.Limit),
				cur.Memory.Failcnt,
				convertToUtil(cur.Memory.ContainerData.Swap, rootCont.Spec.Memory.SwapLimit))

			sort.Sort(cadvisorApi.PerDiskStatsSlice(prev.DiskIo.IoServiced))
			for _, disk := range prev.DiskIo.IoServiced {
				prevIoServiced := disk
				curIoServiced, err := cadvisorApi.PerDiskStatsSlice(cur.DiskIo.IoServiced).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				prevIoServiceBytes, err := cadvisorApi.PerDiskStatsSlice(prev.DiskIo.IoServiceBytes).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				curIoServiceBytes, err := cadvisorApi.PerDiskStatsSlice(cur.DiskIo.IoServiceBytes).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				fmt.Fprintf(b, "%d:%d %d,%d %d,%d ",
					curIoServiced.Major,
					curIoServiced.Minor,
					curIoServiced.Stats["Read"] - prevIoServiced.Stats["Read"],
					(curIoServiceBytes.Stats["Read"] - prevIoServiceBytes.Stats["Read"]) / 1024 / 1024,
					curIoServiced.Stats["Write"] - prevIoServiced.Stats["Write"],
					(curIoServiceBytes.Stats["Write"] - prevIoServiceBytes.Stats["Write"]) / 1024 / 1024)
			}

			fmt.Fprintf(b, "\n")
		}
	}
	fmt.Fprintf(b, "\n")
}

func (cc *cadvisorClient) exportHTTP(port uint) error {
	// Register the handlers regardless as this registers the prometheus
	// collector properly.
	mux := http.NewServeMux()
	err := cadvisorHttp.RegisterHandlers(mux, cc, "", "", "", "")
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`^k8s_(?P<kubernetes_container_name>[^_\.]+)[^_]+_(?P<kubernetes_pod_name>[^_]+)_(?P<kubernetes_namespace>[^_]+)`)
	reCaptureNames := re.SubexpNames()
	cadvisorHttp.RegisterPrometheusHandler(mux, cc, "/metrics", func(name string) map[string]string {
		extraLabels := map[string]string{}
		matches := re.FindStringSubmatch(name)
		for i, match := range matches {
			if len(reCaptureNames[i]) > 0 {
				extraLabels[re.SubexpNames()[i]] = match
			}
		}
		return extraLabels
	})

	// Only start the http server if port > 0
	if port > 0 {
		serv := &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: mux,
		}

		// TODO(vmarmol): Remove this when the cAdvisor port is once again free.
		// If export failed, retry in the background until we are able to bind.
		// This allows an existing cAdvisor to be killed before this one registers.
		go func() {
			defer util.HandleCrash()

			err := serv.ListenAndServe()
			for err != nil {
				glog.Infof("Failed to register cAdvisor on port %d, retrying. Error: %v", port, err)
				time.Sleep(time.Minute)
				err = serv.ListenAndServe()
			}
		}()
	}

	return nil
}

// http://stackoverflow.com/questions/13020308/how-to-fmt-printf-an-integer-with-thousands-comma
func format(n int64) string {
	in := strconv.FormatInt(n, 10)
	out := make([]byte, len(in)+(len(in)-2+int(in[0]/'0'))/3)
	if in[0] == '-' {
		in, out[0] = in[1:], '-'
	}

	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}

