/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package empty_dir

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/types"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/kubernetes/pkg/volume"
	volumeutil "k8s.io/kubernetes/pkg/volume/util"
)

// TODO: in the near future, this will be changed to be more restrictive
// and the group will be set to allow containers to use emptyDir volumes
// from the group attribute.
//
// http://issue.k8s.io/2630
const perm os.FileMode = 0777

// This is the primary entrypoint for volume plugins.
func ProbeVolumePlugins() []volume.VolumePlugin {
	return []volume.VolumePlugin{
		&emptyDirPlugin{nil},
	}
}

type emptyDirPlugin struct {
	host volume.VolumeHost
}

var _ volume.VolumePlugin = &emptyDirPlugin{}

const (
	emptyDirPluginName = "kubernetes.io/empty-dir"
	sigmaDiskBasePath  = "/"
	sigmaDiskAllocated = "sigma-allocated"
	sigmaDiskBackup    = "sigma-backup"
	sigmaDiskMetaFile  = "disk"
	storageMediumDisk  = "Disk"
	invalidPathDiskExhausted = "<diskExhausted>"
)

func (plugin *emptyDirPlugin) Init(host volume.VolumeHost) {
	plugin.host = host
}

func (plugin *emptyDirPlugin) Name() string {
	return emptyDirPluginName
}

func (plugin *emptyDirPlugin) CanSupport(spec *volume.Spec) bool {
	if spec.Volume != nil && spec.Volume.EmptyDir != nil {
		return true
	}
	return false
}

func (plugin *emptyDirPlugin) NewBuilder(spec *volume.Spec, pod *api.Pod, opts volume.VolumeOptions) (volume.Builder, error) {
	return plugin.newBuilderInternal(spec, pod, plugin.host.GetMounter(), &realMountDetector{plugin.host.GetMounter()}, opts, newChconRunner())
}

func (plugin *emptyDirPlugin) newBuilderInternal(spec *volume.Spec, pod *api.Pod, mounter mount.Interface, mountDetector mountDetector, opts volume.VolumeOptions, chconRunner chconRunner) (volume.Builder, error) {
	medium := api.StorageMediumDefault
	if spec.Volume.EmptyDir != nil { // Support a non-specified source as EmptyDir.
		medium = spec.Volume.EmptyDir.Medium
	}
	return &emptyDir{
		pod:           pod,
		volName:       spec.Name(),
		medium:        medium,
		mounter:       mounter,
		mountDetector: mountDetector,
		plugin:        plugin,
		rootContext:   opts.RootContext,
		chconRunner:   chconRunner,
	}, nil
}

func (plugin *emptyDirPlugin) NewCleaner(volName string, podUID types.UID) (volume.Cleaner, error) {
	// Inject real implementations here, test through the internal function.
	return plugin.newCleanerInternal(volName, podUID, plugin.host.GetMounter(), &realMountDetector{plugin.host.GetMounter()})
}

func (plugin *emptyDirPlugin) newCleanerInternal(volName string, podUID types.UID, mounter mount.Interface, mountDetector mountDetector) (volume.Cleaner, error) {
	ed := &emptyDir{
		pod:           &api.Pod{ObjectMeta: api.ObjectMeta{UID: podUID}},
		volName:       volName,
		medium:        api.StorageMediumDefault, // might be changed later
		mounter:       mounter,
		mountDetector: mountDetector,
		plugin:        plugin,
	}
	return ed, nil
}

// mountDetector abstracts how to find what kind of mount a path is backed by.
type mountDetector interface {
	// GetMountMedium determines what type of medium a given path is backed
	// by and whether that path is a mount point.  For example, if this
	// returns (mediumMemory, false, nil), the caller knows that the path is
	// on a memory FS (tmpfs on Linux) but is not the root mountpoint of
	// that tmpfs.
	GetMountMedium(path string) (storageMedium, bool, error)
}

type storageMedium int

const (
	mediumUnknown storageMedium = 0 // assume anything we don't explicitly handle is this
	mediumMemory  storageMedium = 1 // memory (e.g. tmpfs on linux)
)

// EmptyDir volumes are temporary directories exposed to the pod.
// These do not persist beyond the lifetime of a pod.
type emptyDir struct {
	pod           *api.Pod
	volName       string
	medium        api.StorageMedium
	mounter       mount.Interface
	mountDetector mountDetector
	plugin        *emptyDirPlugin
	rootContext   string
	chconRunner   chconRunner
}

// SetUp creates new directory.
func (ed *emptyDir) SetUp() error {
	return ed.SetUpAt(ed.GetPath())
}

// SetUpAt creates new directory.
func (ed *emptyDir) SetUpAt(dir string) error {

	if dir == invalidPathDiskExhausted {
		return fmt.Errorf("disk exhausted")
	}

	notMnt, err := ed.mounter.IsLikelyNotMountPoint(dir)
	// Getting an os.IsNotExist err from is a contingency; the directory
	// may not exist yet, in which case, setup should run.
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// If the plugin readiness file is present for this volume, and the
	// storage medium is the default, then the volume is ready.  If the
	// medium is memory, and a mountpoint is present, then the volume is
	// ready.
	if volumeutil.IsReady(ed.getMetaDir()) {
		if ed.medium == api.StorageMediumMemory && !notMnt {
			return nil
		} else if ed.medium == api.StorageMediumDefault {
			return nil
		} else if ed.medium == storageMediumDisk {
			return nil
		}
	}

	// Determine the effective SELinuxOptions to use for this volume.
	securityContext := ""
	if selinuxEnabled() {
		securityContext = ed.rootContext
	}

	switch ed.medium {
	case api.StorageMediumDefault:
		err = ed.setupDir(dir, securityContext)
	case api.StorageMediumMemory:
		err = ed.setupTmpfs(dir, securityContext)
	case storageMediumDisk:
		err = ed.setupDisk(dir, securityContext)
	default:
		err = fmt.Errorf("unknown storage medium %q", ed.medium)
	}

	if err == nil {
		volumeutil.SetReady(ed.getMetaDir())
		if ed.medium == storageMediumDisk {
			saveDiskPath(ed.getMetaDir(), dir)
		}
	}

	return err
}

func saveDiskPath(metadir, diskpath string) error {
	if err := os.MkdirAll(metadir, 0750); err != nil && !os.IsExist(err) {
		glog.Errorf("Can't mkdir %s: %v", metadir, err)
		return err
	}

	metafile := path.Join(metadir, sigmaDiskMetaFile)
	if err := ioutil.WriteFile(metafile, []byte(diskpath), 0666); err != nil {
		glog.Errorf("Can't save disk path %s: %v", metafile, err)
		return err
	}

	return nil
}

func loadDiskPath(metadir string) (diskpath string, err error) {
	metafile := path.Join(metadir, sigmaDiskMetaFile)
	bytes, err := ioutil.ReadFile(metafile)
	return string(bytes), err
}

func (ed *emptyDir) IsReadOnly() bool {
	return false
}

// setupTmpfs creates a tmpfs mount at the specified directory with the
// specified SELinux context.
func (ed *emptyDir) setupTmpfs(dir string, selinuxContext string) error {
	if ed.mounter == nil {
		return fmt.Errorf("memory storage requested, but mounter is nil")
	}
	if err := ed.setupDir(dir, selinuxContext); err != nil {
		return err
	}
	// Make SetUp idempotent.
	medium, isMnt, err := ed.mountDetector.GetMountMedium(dir)
	if err != nil {
		return err
	}
	// If the directory is a mountpoint with medium memory, there is no
	// work to do since we are already in the desired state.
	if isMnt && medium == mediumMemory {
		return nil
	}

	// By default a tmpfs mount will receive a different SELinux context
	// which is not readable from the SELinux context of a docker container.
	var opts []string
	if selinuxContext != "" {
		opts = []string{fmt.Sprintf("rootcontext=\"%v\"", selinuxContext)}
	} else {
		opts = []string{}
	}

	glog.V(3).Infof("pod %v: mounting tmpfs for volume %v with opts %v", ed.pod.UID, ed.volName, opts)
	return ed.mounter.Mount("tmpfs", dir, "tmpfs", opts)
}

// setupDir creates the directory with the specified SELinux context and
// the default permissions specified by the perm constant.
func (ed *emptyDir) setupDir(dir, selinuxContext string) error {
	// Create the directory if it doesn't already exist.
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}

	// stat the directory to read permission bits
	fileinfo, err := os.Lstat(dir)
	if err != nil {
		return err
	}

	if fileinfo.Mode().Perm() != perm.Perm() {
		// If the permissions on the created directory are wrong, the
		// kubelet is probably running with a umask set.  In order to
		// avoid clearing the umask for the entire process or locking
		// the thread, clearing the umask, creating the dir, restoring
		// the umask, and unlocking the thread, we do a chmod to set
		// the specific bits we need.
		err := os.Chmod(dir, perm)
		if err != nil {
			return err
		}

		fileinfo, err = os.Lstat(dir)
		if err != nil {
			return err
		}

		if fileinfo.Mode().Perm() != perm.Perm() {
			glog.Errorf("Expected directory %q permissions to be: %s; got: %s", dir, perm.Perm(), fileinfo.Mode().Perm())
		}
	}

	// Set the context on the directory, if appropriate
	if selinuxContext != "" {
		glog.V(3).Infof("Setting SELinux context for %v to %v", dir, selinuxContext)
		return ed.chconRunner.SetContext(dir, selinuxContext)
	}

	return nil
}

func (ed *emptyDir) setupDisk(dir, selinuxContext string) error {
	if err := ed.setupDir(dir, selinuxContext); err != nil {
		return err
	}
	// We also need to setup the default path, so Kubelet.getPodVolumesFromDisk is aware of this volume.
	name := emptyDirPluginName
	path := ed.plugin.host.GetPodVolumeDir(ed.pod.UID, util.EscapeQualifiedNameForDisk(name), ed.volName)
	if err := ed.setupDir(path, selinuxContext); err != nil {
		return err
	}
	return nil
}

func (ed *emptyDir) GetPath() string {
	if ed.medium == storageMediumDisk {
		path, err := ed.getPathDisk(sigmaDiskBasePath)
		if err != nil {
			return invalidPathDiskExhausted
		}
		return path
	}

	name := emptyDirPluginName
	return ed.plugin.host.GetPodVolumeDir(ed.pod.UID, util.EscapeQualifiedNameForDisk(name), ed.volName)
}

type diskExhausted struct {
	msg   string // description of error
	alloc int    // number of allocated disks
}

func (e *diskExhausted) Error() string {
	return fmt.Sprintf("Disk Exhausted: allocated %d: %s", e.alloc, e.msg)
}

func (ed *emptyDir) getPodFullName() string {
	return ed.pod.Namespace + "_" + ed.pod.Name
}

func (ed *emptyDir) getPodVolumesDir(disk string) string {
	return path.Join(disk, sigmaDiskAllocated, ed.getPodFullName(), "volumes")
}

func (ed *emptyDir) getPathDisk(basePath string) (string, error) {
	var disks []string
	dirs, err := ioutil.ReadDir(basePath)
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		matched, err := regexp.MatchString("^data[1-9]$", dir.Name())
		if err != nil {
			return "", err
		}
		if matched {
			disks = append(disks, path.Join(basePath, dir.Name()))
		}
	}

	// check if the PodVolumeDir already exists (created previously)
	for _, disk := range disks {
		podVolumesDir := ed.getPodVolumesDir(disk)
		fi, err := os.Stat(podVolumesDir)
		if err == nil && fi.IsDir() { // Found!
			return path.Join(podVolumesDir, ed.volName), nil
		}
	}

	// allocate a disk only if there is no dir'sigmaDiskAllocated'
	for _, disk := range disks {
		if _, err := os.Stat(path.Join(disk, sigmaDiskAllocated)); os.IsNotExist(err) {
			podVolumesDir := ed.getPodVolumesDir(disk)
			return path.Join(podVolumesDir, ed.volName), nil
		}
	}

	// disk exhausted
	return "", &diskExhausted{msg: ed.getPodFullName(), alloc: len(disks)}
}

// TearDown simply discards everything in the directory.
func (ed *emptyDir) TearDown() error {
	return ed.TearDownAt(ed.GetPath())
}

// TearDownAt simply discards everything in the directory.
func (ed *emptyDir) TearDownAt(dir string) error {
	// Figure out the medium if it is disk.
	diskpath, err := loadDiskPath(ed.getMetaDir())
	if err == nil {
		return ed.teardownDisk(diskpath, dir)
	}
	if !os.IsNotExist(err) {
		return err
	}

	// Figure out the medium.
	medium, isMnt, err := ed.mountDetector.GetMountMedium(dir)
	if err != nil {
		return err
	}
	if isMnt && medium == mediumMemory {
		ed.medium = api.StorageMediumMemory
		return ed.teardownTmpfs(dir)
	}
	// assume StorageMediumDefault
	return ed.teardownDefault(dir)
}

func (ed *emptyDir) teardownDisk(diskpath, dir string) error {
	// dir = basePath + disk + sigmaDiskAllocated + podFullName + volumes + volName
	if _, err := os.Stat(diskpath); err == nil {
		// Backup
		n := strings.Index(diskpath, sigmaDiskAllocated)
		if n == -1 {
			return fmt.Errorf("disk storage requested, but disk path is invalid: %q", diskpath)
		}
		tmpDir, err := volume.RenameDirectory(path.Join(diskpath[:n], sigmaDiskAllocated), sigmaDiskBackup)
		if err != nil {
			return err
		}
		glog.Warningf("Orphaned disk volume %q was moved to %q", ed.pod.UID, tmpDir)
	}
	return ed.teardownDefault(dir)
}

func (ed *emptyDir) teardownDefault(dir string) error {
	tmpDir, err := volume.RenameDirectory(dir, ed.volName+".deleting~")
	if err != nil {
		return err
	}
	err = os.RemoveAll(tmpDir)
	if err != nil {
		return err
	}
	return nil
}

func (ed *emptyDir) teardownTmpfs(dir string) error {
	if ed.mounter == nil {
		return fmt.Errorf("memory storage requested, but mounter is nil")
	}
	if err := ed.mounter.Unmount(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

func (ed *emptyDir) getMetaDir() string {
	return path.Join(ed.plugin.host.GetPodPluginDir(ed.pod.UID, util.EscapeQualifiedNameForDisk(emptyDirPluginName)), ed.volName)
}
