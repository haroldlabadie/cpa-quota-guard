package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*,const char*,const uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*,size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*,uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*,size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {uint32_t abi_version;cliproxy_plugin_call_fn call;cliproxy_plugin_free_fn free_buffer;cliproxy_plugin_shutdown_fn shutdown;} cliproxy_plugin_api;
extern int cliproxyPluginCall(char*,uint8_t*,size_t,cliproxy_buffer*);
extern void cliproxyPluginFree(void*,size_t);
extern void cliproxyPluginShutdown(void);
static inline int guard_host_call(cliproxy_host_api* h,const char* m,const uint8_t* b,size_t n,cliproxy_buffer* out){return h->call(h->host_ctx,m,b,n,out);}
static inline void guard_host_free(cliproxy_host_api* h,cliproxy_buffer* out){if(out->ptr)h->free_buffer(out->ptr,out->len);}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"unsafe"
)

var hostAPI C.cliproxy_host_api

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 || host.call == nil || host.free_buffer == nil {
		return 1
	}
	hostAPI = *host
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, n C.size_t, response *C.cliproxy_buffer) (code C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			panicStop()
			writeResponse(response, errorEnvelope("plugin panic; automation stopped"))
			code = 1
		}
	}()
	if method == nil || uint64(n) > 8<<20 {
		writeResponse(response, errorEnvelope("invalid RPC request"))
		return 1
	}
	var raw []byte
	if request != nil && n > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(n))
	}
	result, err := handleMethod(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, errorEnvelope(err.Error()))
		return 1
	}
	writeResponse(response, result)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(p unsafe.Pointer, _ C.size_t) {
	if p != nil {
		C.free(p)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { stopCurrent() }
func writeResponse(out *C.cliproxy_buffer, raw []byte) {
	if len(raw) > 0 {
		out.ptr = C.CBytes(raw)
		out.len = C.size_t(len(raw))
	}
}
func hostCall(method string, in, out any) error {
	if hostAPI.call == nil {
		return errors.New("host RPC unavailable")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return errors.New("host request encoding failed")
	}
	m := C.CString(method)
	defer C.free(unsafe.Pointer(m))
	p := C.CBytes(raw)
	defer C.free(p)
	var b C.cliproxy_buffer
	code := C.guard_host_call(&hostAPI, m, (*C.uint8_t)(p), C.size_t(len(raw)), &b)
	defer C.guard_host_free(&hostAPI, &b)
	if code != 0 || b.ptr == nil || uint64(b.len) > 8<<20 {
		return errors.New("host RPC failed")
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(C.GoBytes(b.ptr, C.int(b.len)), &env) != nil || !env.OK {
		return errors.New("host RPC rejected")
	}
	if out != nil && json.Unmarshal(env.Result, out) != nil {
		return errors.New("host RPC result invalid")
	}
	return nil
}
func okEnvelope(v any) ([]byte, error) { return json.Marshal(map[string]any{"ok": true, "result": v}) }
func errorEnvelope(message string) []byte {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{"code": "guard_error", "message": message}})
	return b
}
