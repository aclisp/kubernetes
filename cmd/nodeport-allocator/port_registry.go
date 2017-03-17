package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sync"
)

// PortRegistry is a mapping from Pod name to port list,
// and it has a state of the allocator
type PortRegistry struct {
	lock    sync.Mutex
	State   string
	PortMap map[string][]int
}

func (reg *PortRegistry) Save(filename string) {
	reg.lock.Lock()
	defer reg.lock.Unlock()

	dir := path.Dir(filename)
	os.MkdirAll(dir, 0755)

	result, err := json.MarshalIndent(reg, "", "    ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v. \n", err)
		return
	}

	err = ioutil.WriteFile(filename, result, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v. \n", err)
		return
	}
}

func (reg *PortRegistry) Load(filename string) {
	reg.lock.Lock()
	defer reg.lock.Unlock()

	file, err := ioutil.ReadFile(filename)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Info: %v. Using empty PortRegistry\n", err)
		return
	}

	err = json.Unmarshal(file, reg)
	if err != nil {
		fmt.Fprintf(os.Stdout, "Info: %v. Using empty PortRegistry\n", err)
		return
	}
}

func (reg *PortRegistry) Find(podname string) ([]int, bool) {
	reg.lock.Lock()
	defer reg.lock.Unlock()

	p, ok := reg.PortMap[podname]
	return p, ok
}

func (reg *PortRegistry) Set(podname string, ports []int, state string) {
	reg.lock.Lock()
	defer reg.lock.Unlock()

	reg.PortMap[podname] = ports
	reg.State = state
}

func (reg *PortRegistry) Delete(podname string, state string) {
	reg.lock.Lock()
	defer reg.lock.Unlock()

	delete(reg.PortMap, podname)
	reg.State = state
}

func newPortRegistry() *PortRegistry {
	reg := &PortRegistry{}
	reg.PortMap = make(map[string][]int)
	return reg
}
