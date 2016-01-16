package hostportallocator

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
)

const (
	portMappingFileExtension = ".portmapping"
	retryAttemps = 10
)

type Allocator struct {
	// listenerHolder is used to prevent Allocate from returning the same port each time you calls it.
	// Although the kernel could usually hand out a different number to each port 0 request, it is
	// wise to not rely on this.
	listenerHolder []net.Listener

	// PodContainerDir is used to save the port mapping of the container
	PodContainerDir string
}

func (allocator *Allocator) Allocate() (port int, err error) {
	ports, err := allocator.getHostPorts()
	if err != nil {
		glog.Warningf("Get existing host ports error: %v", err)
	}

	retries := make(map[int]int)
	for p, _ := range ports {
		retries[p] = retryAttemps
	}

	for {
		left := 0
		for _, c := range retries {
			left += c
		}
		if len(retries) != 0 && left <= 0 {
			break
		}

		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, err
		}
		allocator.listenerHolder = append(allocator.listenerHolder, listener)
		port = listener.Addr().(*net.TCPAddr).Port
		if ports[port] {
			retries[port] = retries[port] - 1
			continue
		}
		return port, nil
	}

	return 0, errors.New("Retry attemps exhausted when allocating host ports.")
}

func (allocator *Allocator) Done() {
	for _, listener := range allocator.listenerHolder {
		listener.Close()
	}
	allocator.listenerHolder = nil
}

func (allocator *Allocator) SavePortMapping(pm kubecontainer.PortMapping) error {
	filePath := path.Join(allocator.PodContainerDir, pm.Name + portMappingFileExtension)
	pmData, err := json.Marshal(pm)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filePath, pmData, 0640)
}

func (allocator *Allocator) LoadPortMapping(pm *kubecontainer.PortMapping) (loaded bool, err error) {
	filePath := path.Join(allocator.PodContainerDir, pm.Name + portMappingFileExtension)
	pmData, err := ioutil.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		} else {
			return false, err
		}
	}

	if err = json.Unmarshal(pmData, pm); err != nil {
		return false, err
	}
	return true, nil
}

func (allocator *Allocator) getHostPorts() (ports map[int]bool, err error) {
	ports = make(map[int]bool)
	err = filepath.Walk(path.Join(allocator.PodContainerDir, "../../.."),
		func (path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(info.Name(), portMappingFileExtension) {
				data, err := ioutil.ReadFile(path)
				if err != nil {
					return nil
				}
				pm := kubecontainer.PortMapping{}
				if err = json.Unmarshal(data, &pm); err != nil {
					return nil
				}
				ports[pm.HostPort] = true
			}
			return nil
		})
	return
}
