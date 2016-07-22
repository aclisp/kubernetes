package stat

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/golang/glog"
	cadvisorapi "github.com/google/cadvisor/info/v1"

	"k8s.io/kubernetes/pkg/kubelet/cadvisor"
	"k8s.io/kubernetes/pkg/kubelet/dockertools"
	"k8s.io/kubernetes/pkg/util/flushsyncwriter"
	"k8s.io/kubernetes/pkg/util/wait"
)

var showStat = flag.Bool("show_stat", false, "Whether to show container statistics on stdout")

type Printer struct {
	count uint
	cadvisor cadvisor.Interface
	buf flushsyncwriter.FlushSyncWriter
}

type debugWriter struct {
	buf flushsyncwriter.FlushSyncWriter
}

func (w *debugWriter) Write(p []byte) (n int, err error) {
	if *showStat {
		os.Stdout.Write(p)
	}
	return w.buf.Write(p)
}

func NewPrinter(cadvisor cadvisor.Interface, logdir string) *Printer {
	flushsyncwriter.LogDir = logdir
	return &Printer{
		cadvisor: cadvisor,
		buf: flushsyncwriter.NewFlushSyncWriter(),
	}
}

func (p *Printer) Start() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, os.Kill, syscall.SIGHUP, syscall.SIGTERM)
	stop := make(chan struct{})
	go wait.Until(func () {
		select {
		case <-sig:
			close(stop)
			p.buf.Flush()
			p.buf.Sync()
			os.Exit(0)
		default:
		}
		p.print()
		p.count++
	}, 1*time.Second, stop)
}

func (p *Printer) print() {
	b := &debugWriter{p.buf}
	// Get machine info and root container info
	machine, err := p.cadvisor.MachineInfo()
	if err != nil {
		glog.Fatal(err)
	}
	rootCont, err := p.cadvisor.ContainerInfo("/", &cadvisorapi.ContainerInfoRequest{NumStats:1})
	if err != nil {
		glog.Fatal(err)
	}
	// Print root container
	if p.count % 60 == 0 {
		fmt.Fprintf(b, "/ root container")
		fmt.Fprintf(b, " CPU core %s", format(int64(machine.NumCores * 1000)))
		fmt.Fprintf(b, " MEM max-swa %s %s", format(int64(rootCont.Spec.Memory.Limit)), format(int64(rootCont.Spec.Memory.SwapLimit)))
		fmt.Fprintf(b, "\n")
	}
	// Get and sort all docker containers
	containers, err := p.cadvisor.AllDockerContainers(&cadvisorapi.ContainerInfoRequest{NumStats:2})
	if err != nil {
		glog.Fatal(err)
	}
	var contNames []string
	for contName := range containers {
		contNames = append(contNames, contName)
	}
	sort.Strings(contNames)
	// Excludes k8s POD container and data-volume container
	excludes := map[string]bool{
		"POD": true,
		"data-volume": true,
	}
	// Print every container in a loop
	for _, contName := range contNames {
		cont := containers[contName]
		// Extract pod full name and pod container name from raw docker name
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
		if p.count % 20 == 0 {
			fmt.Fprintf(b, "%s %s %s", cont.Name[:20], podFullName, podContainerName)
			fmt.Fprintf(b, " CPU req-lim %s %s", format(int64(contSpecCpuLimit)), format(int64(contSpecCpuMaxLimit)))
			fmt.Fprintf(b, " MEM lim-swa %s %s", format(int64(cont.Spec.Memory.Limit)), format(int64(cont.Spec.Memory.SwapLimit)))
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
				if limitValue == 0 {
					return 0
				}
				return curValue * 100 / limitValue
			}
			cpuUserRate := convertToRate(prev.Cpu.Usage.User, cur.Cpu.Usage.User)
			cpuSystemRate := convertToRate(prev.Cpu.Usage.System, cur.Cpu.Usage.System)
			cpuTotalRate := convertToRate(prev.Cpu.Usage.Total, cur.Cpu.Usage.Total)
			fmt.Fprintf(b, "  %s %s | %2d,%2d %2d,%2d %2d,%2d %2d | ",
				cur.Timestamp.Format(time.Stamp),
				podFullName,
				cpuUserRate / 100,
				convertToUtil(cpuUserRate, contSpecCpuMaxLimit),
				cpuSystemRate / 100,
				convertToUtil(cpuSystemRate, contSpecCpuMaxLimit),
				cpuTotalRate / 100,
				convertToUtil(cpuTotalRate, contSpecCpuMaxLimit),
				cur.Cpu.Throttling.ThrottledPeriods)
			fmt.Fprintf(b, "%2d %2d %2d %2d %2d | ",
				convertToUtil(cur.Memory.WorkingSet, cont.Spec.Memory.Limit),
				convertToUtil(cur.Memory.Usage, cont.Spec.Memory.Limit),
				convertToUtil(cur.Memory.MaxUsage, cont.Spec.Memory.Limit),
				cur.Memory.Failcnt,
				convertToUtil(cur.Memory.ContainerData.Swap, rootCont.Spec.Memory.SwapLimit))
			sort.Sort(cadvisorapi.PerDiskStatsSlice(prev.DiskIo.IoServiced))
			for _, disk := range prev.DiskIo.IoServiced {
				prevIoServiced := disk
				curIoServiced, err := cadvisorapi.PerDiskStatsSlice(cur.DiskIo.IoServiced).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				prevIoServiceBytes, err := cadvisorapi.PerDiskStatsSlice(prev.DiskIo.IoServiceBytes).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				curIoServiceBytes, err := cadvisorapi.PerDiskStatsSlice(cur.DiskIo.IoServiceBytes).Find(disk.Major, disk.Minor)
				if err != nil {
					continue
				}
				diskName := fmt.Sprintf("%d:%d", curIoServiced.Major, curIoServiced.Minor)
				if diskInfo, ok := machine.DiskMap[diskName]; ok {
					diskName = diskInfo.Name
				} else {
					continue
				}
				fmt.Fprintf(b, "%s %d,%d %d,%d ",
					diskName,
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
