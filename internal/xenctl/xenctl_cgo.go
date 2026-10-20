//go:build libxl

// C-level domain creation. The xenlight binding's generated toC copies our
// Go fields onto malloc'd C structs it never runs libxl_*_init on, leaving
// every field the Go struct cannot express (the libxl_device_disk union,
// all optional string pointers, enum "unknown" sentinels) as raw malloc
// garbage. libxl__device_disk_setdefault then fails ("Unable to set disk
// defaults for disk 0") and the subsequent config dispose frees the garbage
// pointer (heap abort). Assembling the config in C with the public init
// functions is the correct path; the binding is still used for the
// read-only APIs (list, name<->domid, destroy, shutdown).
package xenctl

/*
#cgo LDFLAGS: -lxenlight -lyajl -lxentoollog
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <libxl.h>
#include <libxl_utils.h>

// Save the current SIGCHLD disposition and install a bare SIG_IGN
// (sa_flags=0) for the duration of a create; qlvm_chld_restore puts the
// saved disposition back. libxl's sigchld_installhandler_core
// (libxl_fork.c) asserts the previous action was SIG_DFL or SIG_IGN
// WITHOUT SA_SIGINFO. Go's os/signal.Ignore is not sufficient: the
// runtime's setsig installs SIG_IGN with the trampoline's
// SA_SIGINFO|SA_ONSTACK|SA_RESTORER|SA_RESTART flags still set, which
// trips the assert (SIGABRT on vm start, 2026-10-19). createMu keeps at
// most one save window open at a time, so a static is safe.
static struct sigaction qlvm_chld_saved;
static void qlvm_chld_ignore(void) {
    struct sigaction sa;
    sigaction(SIGCHLD, NULL, &qlvm_chld_saved);
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = SIG_IGN;
    sigaction(SIGCHLD, &sa, NULL);
}
static void qlvm_chld_restore(void) {
    sigaction(SIGCHLD, &qlvm_chld_saved, NULL);
}

// libxl runs the vif script as our child during create. Use the
// libxl-owned SIGCHLD path (self-pipe): libxl installs its own handler
// while it has children and reaps them inside ev_dispatch, on the
// create thread. The mainloop owner + a Go-goroutine forwarder (the
// xenlight Go binding's design) reaps from a different thread than
// ev_dispatch and crashed with a use-after-free when vif-ovn exited
// (2026-10-19). libxl saves/restores the previous handler around its
// ownership window, so other Go children (losetup, exec helpers) keep
// working: their Wait uses a blocking wait4 that does not depend on
// receiving the signal.
static const libxl_childproc_hooks qlvm_childproc_hooks = { .chldowner = libxl_sigchld_owner_libxl };

static void qlvm_set_chldproc(libxl_ctx *ctx) {
	libxl_childproc_setmode(ctx, &qlvm_childproc_hooks, NULL);
}

static char *qlvm_qstr(const char *s) { return s ? strdup(s) : NULL; }

int qlvm_create_domain(libxl_ctx *ctx,
                       const char *name,
                       const unsigned char *uuid,
                       const char *kernel,
                       const char *ramdisk,
                       const char *cmdline,
                       int maxvcpus,
                       unsigned long long memkb,
                       const char *diskpath,
                       int diskrw,
                       const unsigned char *mac,
                       const char *vifscript,
                       int np9,
                       const char **p9tags,
                       const char **p9paths,
                       const char **p9models,
                       int ncpus,
                       const int *cpus,
                       unsigned int *domid)
{
    libxl_domain_config d;
    libxl_device_disk *disks;
    libxl_device_nic *nics;
    int i, rc;

    libxl_domain_config_init(&d);

    d.c_info.name = qlvm_qstr(name);
    memcpy(d.c_info.uuid.uuid, uuid, 16);
    d.c_info.type = LIBXL_DOMAIN_TYPE_PVH;

    d.b_info.type = LIBXL_DOMAIN_TYPE_PVH;
    memset(&d.b_info.u, 0, sizeof(d.b_info.u));
    d.b_info.max_vcpus = maxvcpus;
    // Boot all vCPUs online: xl's vcpus= maps to the avail_vcpus bitmap,
    // and an unset bitmap defaults to 1 online vCPU despite max_vcpus (the
    // A3 symptom). config_dispose below frees the map.
    {
        uint32_t nbytes = (uint32_t)((maxvcpus + 7) / 8);
        d.b_info.avail_vcpus.size = nbytes;
        d.b_info.avail_vcpus.map = nbytes ? calloc(1, nbytes) : NULL;
        for (i = 0; i < maxvcpus; i++)
            d.b_info.avail_vcpus.map[i / 8] |= (uint8_t)(1u << (i % 8));
    }
    d.b_info.target_memkb = memkb;
    // max_memkb defaults to a 32MB stub when left unset; pin it to the
    // requested memory. shadow_memkb is left at the generated-init
    // sentinel so libxl computes the PVH default during create (setting
    // 0 explicitly makes libxc reject the build: "Failed to set paging
    // mempool size to 0kB: Cannot allocate memory").
    // NOTE: use only line comments in this block; a block comment would
    // close the cgo comment early and the Go compiler would see C code.
    d.b_info.max_memkb = memkb;
    d.b_info.kernel = qlvm_qstr(kernel);
    d.b_info.ramdisk = qlvm_qstr(ramdisk);
    d.b_info.cmdline = qlvm_qstr(cmdline);

    // CPU pinning. libxl_domain_config_init leaves b_info.cpumap empty
    // (map=NULL, size=0); libxl_cpu_bitmap_alloc(ctx, b, 0) allocates it
    // at machine size, which is what libxl's create path requires: it
    // converts the domain cpumap into per-vCPU hard affinity by copying
    // it into machine-sized bitmaps and asserts on a size mismatch
    // (libxl_bitmap_copy). Writing the bits on the empty map crashed the
    // first start of a core-pinned VM (SIGSEGV, 2026-10-19); a short
    // (max_cpu + 8) / 8 buffer hit the size assertion.
    if (ncpus > 0 && cpus != NULL) {
        rc = libxl_cpu_bitmap_alloc(ctx, &d.b_info.cpumap, 0);
        if (rc != 0) {
            libxl_domain_config_dispose(&d);
            // ctx is owned by the Go side (cgoCtx.Close frees it).
            return rc;
        }
        for (i = 0; i < ncpus; i++) {
            libxl_bitmap_set(&d.b_info.cpumap, cpus[i]);
        }
        // Pre-populate vcpu_hard_affinity with plain mallocs so
        // libxl__domain_build_info_setdefault (libxl_create.c:188) skips
        // its own cpumap->vcpu_hard_affinity conversion, which allocates
        // the array from the AO's GC arena. That arena is freed when the
        // AO is destroyed at the end of create_new, leaving a dangling
        // array that libxl_domain_config_dispose would free a second
        // time (double free / SIGSEGV, 2026-10-19). The conversion is
        // skipped when num_vcpu_hard_affinity != 0; the values are
        // identical (machine-sized copies of the cpumap).
        d.b_info.vcpu_hard_affinity = calloc(maxvcpus, sizeof(libxl_bitmap));
        for (i = 0; i < maxvcpus; i++) {
            if (libxl_cpu_bitmap_alloc(ctx, &d.b_info.vcpu_hard_affinity[i], 0)) {
                libxl_domain_config_dispose(&d);
                return ERROR_FAIL;
            }
            libxl_bitmap_copy(ctx, &d.b_info.vcpu_hard_affinity[i], &d.b_info.cpumap);
        }
        d.b_info.num_vcpu_hard_affinity = maxvcpus;
    }

    disks = calloc(1, sizeof *disks);
    libxl_device_disk_init(&disks[0]);
    disks[0].pdev_path = qlvm_qstr(diskpath);
    disks[0].vdev = qlvm_qstr("xvda");
    disks[0].format = LIBXL_DISK_FORMAT_RAW;
    disks[0].readwrite = diskrw;
    d.disks = disks;
    d.num_disks = 1;

    nics = calloc(1, sizeof *nics);
    libxl_device_nic_init(&nics[0]);
    memcpy(nics[0].mac, mac, 6);
    nics[0].script = qlvm_qstr(vifscript);
    nics[0].nictype = LIBXL_NIC_TYPE_VIF;
    d.nics = nics;
    d.num_nics = 1;

    if (np9 > 0) {
        d.p9s = calloc(np9, sizeof *d.p9s);
        for (i = 0; i < np9; i++) {
            libxl_device_p9_init(&d.p9s[i]);
            d.p9s[i].tag = qlvm_qstr(p9tags[i]);
            d.p9s[i].path = qlvm_qstr(p9paths[i]);
            d.p9s[i].security_model = qlvm_qstr(p9models[i]);
            d.p9s[i].type = LIBXL_P9_TYPE_XEN_9PFSD; // NOLINT: C field named `type`
        }
        d.num_p9s = np9;
    }

    rc = libxl_domain_create_new(ctx, &d, domid, NULL, NULL);
    libxl_domain_config_dispose(&d);
    // libxl 4.21 leaves the new domain's vcpus paused when create_new
    // returns; xl create unpauses explicitly (xl_vmcontrol.c) before
    // returning. Without this the guest never executes (vCPU time 0).
    if (rc == 0) {
        rc = libxl_domain_unpause(ctx, *domid, NULL);
    }
    return rc;
}
*/
import "C"

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// cgoCtx is a standalone libxl context (the binding's context pointer is
// unexported) with its own logger. SIGCHLD needs no Go forwarder: libxl
// owns the signal itself while it has children (see qlvm_childproc_hooks).
type cgoCtx struct {
	ctx    *C.libxl_ctx
	logger *C.xentoollog_logger_stdiostream
}

func newCGOContext() (*cgoCtx, error) {
	c := &cgoCtx{}
	var loglvl C.xentoollog_level = C.XTL_ERROR
	if os.Getenv("QLVM_LIBXL_LOG") == "debug" {
		loglvl = C.XTL_DEBUG
	}
	c.logger = C.xtl_createlogger_stdiostream(C.stderr, loglvl, 0)
	if r := C.libxl_ctx_alloc(&c.ctx, C.LIBXL_VERSION, 0, (*C.xentoollog_logger)(unsafe.Pointer(c.logger))); r != 0 {
		C.xtl_logger_destroy((*C.xentoollog_logger)(unsafe.Pointer(c.logger)))
		return nil, fmt.Errorf("libxl_ctx_alloc: rc=%d", r)
	}
	C.qlvm_set_chldproc(c.ctx)
	return c, nil
}

func (c *cgoCtx) close() {
	C.libxl_ctx_free(c.ctx)
	C.xtl_logger_destroy((*C.xentoollog_logger)(unsafe.Pointer(c.logger)))
}

// cgoCreateDomain creates the domain described by spec (PVH, raw disks, one
// vif-ovn NIC, optional p9 shares) via the C path above.
func cgoCreateDomain(spec *vm.DomainSpec) error {
	c, err := newCGOContext()
	if err != nil {
		return err
	}
	defer c.close()

	name := C.CString(spec.Name)
	defer C.free(unsafe.Pointer(name))
	kernel := C.CString(spec.Kernel)
	defer C.free(unsafe.Pointer(kernel))
	ramdisk := C.CString(spec.Ramdisk)
	defer C.free(unsafe.Pointer(ramdisk))
	cmdline := C.CString(strings.Join(spec.Extra, " "))
	defer C.free(unsafe.Pointer(cmdline))
	disk := C.CString(spec.Disks[0].PdevPath)
	defer C.free(unsafe.Pointer(disk))
	script := C.CString(spec.Nics[0].Script)
	defer C.free(unsafe.Pointer(script))

	uuidb, err := parseUUID(spec.UUID)
	if err != nil {
		return err
	}
	macb, err := parseMAC(spec.Nics[0].Mac)
	if err != nil {
		return err
	}

	var p9tags, p9paths, p9models []unsafe.Pointer
	for _, p := range spec.P9S {
		t, pa, m := C.CString(p.Tag), C.CString(p.Path), C.CString(p.SecurityModel)
		p9tags = append(p9tags, unsafe.Pointer(t))
		p9paths = append(p9paths, unsafe.Pointer(pa))
		p9models = append(p9models, unsafe.Pointer(m))
		defer C.free(unsafe.Pointer(t))
		defer C.free(unsafe.Pointer(pa))
		defer C.free(unsafe.Pointer(m))
	}
	var p9t, p9p, p9m unsafe.Pointer
	if len(p9tags) > 0 {
		p9t = unsafe.Pointer(&p9tags[0])
		p9p = unsafe.Pointer(&p9paths[0])
		p9m = unsafe.Pointer(&p9models[0])
	}

	// CPU pinning: convert spec.CPUs to a C int array.
	var cCpus *C.int
	var nCpus C.int
	if len(spec.CPUs) > 0 {
		slice := make([]C.int, len(spec.CPUs))
		for i, c := range spec.CPUs {
			slice[i] = C.int(c)
		}
		cCpus = &slice[0]
		nCpus = C.int(len(slice))
	}

	// libxl (chldowner=libxl) installs its own SIGCHLD handler when it
	// forks the first hotplug helper, and asserts the previous
	// disposition was SIG_DFL or SIG_IGN without SA_SIGINFO
	// (libxl_fork.c). The Go runtime always installs its own SIGCHLD
	// trampoline, so for the duration of the call save the disposition
	// and install a bare SIG_IGN (qlvm_chld_ignore). libxl saves that
	// disposition and restores it when its last child is reaped; the
	// restore below returns whatever we saved. createMu serializes
	// concurrent creates: libxl's SIGCHLD install state is
	// process-global, as is our save/restore window.
	createMu.Lock()
	C.qlvm_chld_ignore()
	var domid C.uint
	rc := C.qlvm_create_domain(
		c.ctx,
		name,
		(*C.uchar)(unsafe.Pointer(&uuidb[0])),
		kernel,
		ramdisk,
		cmdline,
		C.int(spec.MaxVcpus),
		C.ulonglong(spec.TargetMemkb),
		disk,
		C.int(spec.Disks[0].Readwrite),
		(*C.uchar)(unsafe.Pointer(&macb[0])),
		script,
		C.int(len(spec.P9S)),
		(**C.char)(p9t),
		(**C.char)(p9p),
		(**C.char)(p9m),
		nCpus,
		cCpus,
		&domid,
	)
	C.qlvm_chld_restore()
	createMu.Unlock()
	if rc != 0 {
		return fmt.Errorf("libxl create %s: rc=%d (see libxl log above)", spec.Name, rc)
	}
	return nil
}

// createMu serializes cgoCreateDomain (see the qlvm_chld_ignore comment).
var createMu sync.Mutex
