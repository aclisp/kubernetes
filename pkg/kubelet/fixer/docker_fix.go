package fixer

import (
	"k8s.io/kubernetes/pkg/util/dbus"
	"k8s.io/kubernetes/pkg/util/exec"
	"k8s.io/kubernetes/pkg/util/iptables"
)

type systemUtil struct {
	iptablesInterface iptables.Interface
}

func newSystemUtil() systemUtil {
	// Create a iptables utils.
	execer := exec.New()
	dbuser := dbus.New()
	protocol := iptables.ProtocolIpv4
	iptInterface := iptables.New(execer, dbuser, protocol)
	return systemUtil{
		iptablesInterface: iptInterface,
	}
}

var system = newSystemUtil()

// The node guard may periodically remove all rules in iptables filter chain.
// EnsureDockerRules ensures following rules are always present:
// -N DOCKER
// -A FORWARD -o docker0 -j DOCKER
// -A FORWARD -o docker0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
// -A FORWARD -i docker0 ! -o docker0 -j ACCEPT
// -A FORWARD -i docker0 -o docker0 -j ACCEPT
// This function is used to fix this issue:
//W0204 11:23:28.715832  203985 kubelet.go:1030] Load previous port mapping "POD-ssh" from "/data/kubelet/pods/71b2e2ee-caee-11e5-b102-6c92bf223434/containers/POD"
//W0204 11:23:28.715931  203985 kubelet.go:1030] Load previous port mapping "POD-http" from "/data/kubelet/pods/71b2e2ee-caee-11e5-b102-6c92bf223434/containers/POD"
//E0204 11:23:30.478123  203985 manager.go:1847] Failed to create pod infra container: API error (500): Cannot start container 90da3598610f276eaa643cb19775a5450054729126d17195f70b322f1966a6ad
//: iptables failed: iptables -t filter -A DOCKER ! -i docker0 -o docker0 -p tcp -d 172.17.0.26 --dport 8080 -j ACCEPT: iptables: No chain/target/match by that name.
//(exit status 1)
//; Skipping pod "vm-huanghao-test-me_default"
//E0204 11:23:30.519353  203985 pod_workers.go:112] Error syncing pod 71b2e2ee-caee-11e5-b102-6c92bf223434, skipping: API error (500): Cannot start container 90da3598610f276eaa643cb19775a5450
//054729126d17195f70b322f1966a6ad: iptables failed: iptables -t filter -A DOCKER ! -i docker0 -o docker0 -p tcp -d 172.17.0.26 --dport 8080 -j ACCEPT: iptables: No chain/target/match by that
//name.
//(exit status 1)
func EnsureDockerRules() {
	bridgeIface := "docker0"
	system.iptablesInterface.EnsureRule(iptables.Prepend, "filter", "FORWARD", []string{"-i", bridgeIface, "-o", bridgeIface, "-j", "ACCEPT"}...)
	system.iptablesInterface.EnsureRule(iptables.Prepend, "filter", "FORWARD", []string{"-i", bridgeIface, "!", "-o", bridgeIface, "-j", "ACCEPT"}...)
	system.iptablesInterface.EnsureRule(iptables.Prepend, "filter", "FORWARD", []string{"-o", bridgeIface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}...)
	system.iptablesInterface.EnsureChain("filter", "DOCKER")
	system.iptablesInterface.EnsureRule(iptables.Prepend, "filter", "FORWARD", []string{"-o", bridgeIface, "-j", "DOCKER"}...)
}
