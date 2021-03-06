package main

import (
	"encoding/json"
	"fmt"
	flag "github.com/spf13/pflag"
	"io/ioutil"
	k8sapi "k8s.io/kubernetes/pkg/api"
	k8snet "k8s.io/kubernetes/pkg/util/net"
	k8swait "k8s.io/kubernetes/pkg/util/wait"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

var (
	configFile = flag.String("config-file", "/etc/k8s/nodeport/config.json", "Config file to read at start.")
	dataFile   = flag.String("data-file", "/etc/k8s/nodeport/data.json", "Data file stores the allocated ports.")
)

type Config struct {
	// portRange is the range of host ports (beginPort-endPort, inclusive) that may be consumed
	// in order to allocate port from. If unspecified (0-0) then ports will be randomly chosen.
	PortRange  string
	ListenPort int
}

var (
	allocator PortAllocator
	registry  *PortRegistry
)

// PodPorts is used as the returned value of AllocateOrGetPorts
type PodPorts struct {
	PodName string
	Ports   []int
}

// GetPorts query (if num<=0) or allocate ports for podname.
// If the ports are already allocated, it returns them as is.
func GetPorts(podname string, num int) (PodPorts, error) {
	ports, ok := registry.Find(podname)
	if ok {
		return PodPorts{
			PodName: podname,
			Ports:   ports,
		}, nil
	}

	if num > 0 {
		return allocatePorts(podname, num)
	}

	return PodPorts{
		PodName: podname,
		Ports:   nil,
	}, fmt.Errorf("Ports are not allocated for %q", podname)
}

func allocatePorts(podname string, num int) (PodPorts, error) {
	var ports []int
	var err error

	for num > 0 {
		var port int
		port, err = allocator.AllocateNext()
		if err != nil {
			break
		}

		ports = append(ports, port)
		num--
	}

	if err != nil {
		// rollback if err
		for _, port := range ports {
			allocator.Release(port)
		}
		ports = nil
	} else {
		// remember this allocation if success
		registry.Set(podname, ports, allocator.GetState())
		registry.Save(*dataFile)
	}

	return PodPorts{
		PodName: podname,
		Ports:   ports,
	}, err
}

// DeletePorts deletes from registry the ports allocated for podname.
func DeletePorts(podname string) {
	ports, ok := registry.Find(podname)
	if !ok {
		return
	}

	for _, port := range ports {
		allocator.Release(port)
	}

	registry.Delete(podname, allocator.GetState())
	registry.Save(*dataFile)
}

// updateConfig will update conf according to flags
func updateConfig(conf *Config) {
	flag.Parse()

	if *configFile == "" {
		flag.Usage()
		os.Exit(2)
	}

	file, err := ioutil.ReadFile(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Info: %v. Using default config %+v\n", err, *conf)
		return
	}

	err = json.Unmarshal(file, conf)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Info: %v. Using default config %+v\n", err, *conf)
		return
	}

	fmt.Fprintf(os.Stdout, "Using config %+v\n", *conf)
}

func setupPortAllocator(conf Config) {
	var pr k8snet.PortRange
	err := pr.Set(conf.PortRange)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}
	allocator = newPortAllocator(pr)
}

// setupSignalHandlers runs a goroutine that waits on SIGINT or SIGTERM and logs it
// program will be terminated by SIGKILL when grace period ends.
func setupSignalHandlers() {
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGHUP,
		syscall.SIGKILL,
		syscall.SIGPIPE,
		syscall.SIGALRM,
		syscall.SIGUSR1,
		syscall.SIGUSR2)
	go func() {
		fmt.Fprintf(os.Stdout, "Received signal: %q, will exit when the grace period ends\n", <-sigChan)
		registry.Save(*dataFile)
		os.Exit(0)
	}()
}

func setupHttpHandlers() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "healthz ok\n")
	})
	http.HandleFunc("/getports", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		// Validate parameters
		podName := query.Get("pod")
		if podName == "" {
			http.Error(w, "Need pod name", http.StatusBadRequest)
			return
		}
		num := query.Get("num")
		if num == "" {
			http.Error(w, fmt.Sprintf("How many ports do you want for %q", podName), http.StatusBadRequest)
			return
		}
		numInt, err := strconv.Atoi(num)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid ports number: %q", num), http.StatusBadRequest)
			return
		}

		// Do allocation
		pts, err := GetPorts(podName, numInt)
		if err != nil {
			http.Error(w, fmt.Sprintf("Couldn't allocate or get ports: %v", err), http.StatusInternalServerError)
			return
		}
		result, err := json.MarshalIndent(pts, "", "    ")
		if err != nil {
			http.Error(w, fmt.Sprintf("Couldn't marshal result: %+v", pts), http.StatusInternalServerError)
			return
		}

		// Output result
		fmt.Fprintf(w, "%s\n", result)
	})
	http.HandleFunc("/deleteports", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		// Validate parameters
		podName := query.Get("pod")
		if podName == "" {
			http.Error(w, "Need pod name", http.StatusBadRequest)
			return
		}

		DeletePorts(podName)
		fmt.Fprintf(w, "deleteports ok\n")
	})
}

func setupHttpServer(conf Config) {
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", conf.ListenPort), nil))
}

func setupPortRegistry() {
	registry = newPortRegistry()
	registry.Load(*dataFile)

	if registry.State != "" {
		allocator.SetState(registry.State)
	}
}

func portGarbageCollectOnce() {
	resp, err := http.Get("http://127.0.0.1:10255/pods")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var pods k8sapi.PodList
	err = decoder.Decode(&pods)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	runningPods := make(map[string]bool)
	for _, pod := range pods.Items {
		runningPods[pod.Name] = true
	}

	var toBeDeletedPods []string
	registry.Enum(func(podname string, _ []int) {
		// If the pod exists in registry but not running, add it to list
		if !runningPods[podname] {
			toBeDeletedPods = append(toBeDeletedPods, podname)
		}
	})

	for _, pod := range toBeDeletedPods {
		DeletePorts(pod)
	}
}

func setupPortGarbageCollector() {
	go k8swait.Until(portGarbageCollectOnce, 5*time.Second, k8swait.NeverStop)
}

func main() {
	// Init and set the default value of conf
	conf := Config{
		PortRange:  "18001-18999",
		ListenPort: 4195,
	}

	// Customize conf according to flags
	updateConfig(&conf)

	// Setup the port allocator
	setupPortAllocator(conf)

	// Setup the port registry
	setupPortRegistry()

	// Setup signal handler so that port allocation is persisted when exit
	setupSignalHandlers()

	// Setup HTTP API handlers
	setupHttpHandlers()

	// Setup port garbage collector
	setupPortGarbageCollector()

	// Setup HTTP server
	setupHttpServer(conf)

	fmt.Fprintf(os.Stdout, "main exit\n")
}
