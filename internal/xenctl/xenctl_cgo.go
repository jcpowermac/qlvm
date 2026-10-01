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
#include <stdlib.h>
#include <string.h>
#include <libxl.h>
#include <libxl_utils.h>

// Same childproc setup as the xenlight binding's preamble (4.21 API):
// libxl runs the vif script as our child during create and expects the
// owning process to forward SIGCHLD to it.
static const libxl_childproc_hooks qlvm_childproc_hooks = { .chldowner = libxl_sigchld_owner_mainloop };

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
	"os/signal"
	"strings"
	"syscall"
	"unsafe"

	"github.com/jcpowermac/qlvm/internal/vm"
)

// cgoCtx is a standalone libxl context (the binding's context pointer is
// unexported). It mirrors the binding's NewContext: its own logger, its own
// SIGCHLD forwarder, because libxl spawns the vif script as a child during
// create and expects libxl_childproc_sigchld_occurred on its context.
type cgoCtx struct {
	ctx     *C.libxl_ctx
	logger  *C.xentoollog_logger_stdiostream
	sigchld chan os.Signal
	done    chan struct{}
}

func newCGOContext() (*cgoCtx, error) {
	c := &cgoCtx{}
	c.logger = C.xtl_createlogger_stdiostream(C.stderr, C.XTL_ERROR, 0)
	if r := C.libxl_ctx_alloc(&c.ctx, C.LIBXL_VERSION, 0, (*C.xentoollog_logger)(unsafe.Pointer(c.logger))); r != 0 {
		C.xtl_logger_destroy((*C.xentoollog_logger)(unsafe.Pointer(c.logger)))
		return nil, fmt.Errorf("libxl_ctx_alloc: rc=%d", r)
	}
	C.qlvm_set_chldproc(c.ctx)
	c.sigchld = make(chan os.Signal, 2)
	c.done = make(chan struct{}, 1)
	signal.Notify(c.sigchld, syscall.SIGCHLD)
	go func() {
		for range c.sigchld {
			C.libxl_childproc_sigchld_occurred(c.ctx)
		}
		close(c.done)
	}()
	return c, nil
}

func (c *cgoCtx) close() {
	signal.Stop(c.sigchld)
	close(c.sigchld)
	<-c.done
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
		&domid,
	)
	if rc != 0 {
		return fmt.Errorf("libxl create %s: rc=%d (see libxl log above)", spec.Name, rc)
	}
	return nil
}
