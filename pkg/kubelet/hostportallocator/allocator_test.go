package hostportallocator

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"testing"

	"k8s.io/kubernetes/pkg/api"
	kubecontainer "k8s.io/kubernetes/pkg/kubelet/container"
)

func TestAllocateAndDone(t *testing.T) {
	allocator := Allocator{
		PodContainerDir: "/tmp/pods/a/containers/3",
	}
	if err := os.MkdirAll(allocator.PodContainerDir, 0755); err != nil {
		t.Fatal(err)
	}

	count := 10
	for i := 1; i <= count; i++ {
		port, err := allocator.Allocate()
		if err != nil {
			t.Errorf("Can not allocate: %v", err)
		}
		t.Logf("%d: Allocated port: %d", i, port)
	}

	size := len(allocator.listenerHolder)
	if size != count {
		t.Logf("Just allocated %d times, but get %d listeners.", count, size)
	}

	allocator.Done()
	size = len(allocator.listenerHolder)
	if size != 0 {
		t.Errorf("Call allocator.Done, should have 0 listeners.")
	}

	port, err := allocator.Allocate()
	if err != nil {
		t.Errorf("Can not allocate: %v", err)
	}
	t.Logf("Again: Allocated port: %d", port)
	allocator.Done()
}

func TestSavePortMapping(t *testing.T) {
	allocator := Allocator{
		PodContainerDir: "/tmp",
	}

	pm := kubecontainer.PortMapping{
		Name: "container-someport",
		Protocol: api.ProtocolTCP,
		ContainerPort: 5000,
		HostPort: 62000,
	}
	err := allocator.SavePortMapping(pm)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadPortMapping(t *testing.T) {
	allocator := Allocator{
		PodContainerDir: "/tmp",
	}

	pmSave := kubecontainer.PortMapping{
		Name: "container-someport",
		Protocol: api.ProtocolTCP,
		ContainerPort: 5000,
		HostPort: 62000,
	}
	err := allocator.SavePortMapping(pmSave)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	pmLoad := kubecontainer.PortMapping{
		Name: "container-someport",
	}
	loaded, err := allocator.LoadPortMapping(&pmLoad)
	if loaded != true {
		t.Errorf("unexpected: loaded = %t", loaded)
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if pmSave.Protocol != pmLoad.Protocol ||
		pmSave.ContainerPort != pmLoad.ContainerPort ||
		pmSave.HostPort != pmLoad.HostPort ||
		pmSave.HostIP != pmLoad.HostIP {
		t.Errorf("what load is not equal to save")
	}
}

func TestLoadNotExists(t *testing.T) {
	allocator := Allocator{
		PodContainerDir: "/tmp",
	}
	pmLoad := kubecontainer.PortMapping{
		Name: "container-someport",
	}
	os.Remove(path.Join(allocator.PodContainerDir, pmLoad.Name + portMappingFileExtension))

	loaded, err := allocator.LoadPortMapping(&pmLoad)
	if loaded != false {
		t.Errorf("unexpected: loaded = %t", loaded)
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetHostPorts(t *testing.T) {
	dirs := []string{
		"/tmp/pods/a/containers/1",
		"/tmp/pods/a/containers/2",
		"/tmp/pods/a/containers/3",
		"/tmp/pods/b/containers/1",
		"/tmp/pods/b/containers/2",
		"/tmp/pods/c/containers/1",
		"/tmp/pods/d/containers",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]kubecontainer.PortMapping{
		"/tmp/pods/a/containers/1/x.portmapping": { HostPort: 58950 },
		"/tmp/pods/a/containers/3/y.portmapping": { HostPort: 58951 },
		"/tmp/pods/c/containers/1/z.portmapping": { HostPort: 58952 },
	}
	for file, pm := range files {
		data, err := json.Marshal(pm)
		if err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(file, data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	allocator := Allocator{
		PodContainerDir: "/tmp/pods/a/containers/3",
	}
	ports, err := allocator.getHostPorts()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(ports) != 3 {
		t.Errorf("expect get 3 host ports")
	}
	t.Logf("host ports are %v", ports)
}
