#include <windows.h>

typedef BOOLEAN (APIENTRY *RtlGenRandomFn)(PVOID, ULONG);

BOOL WINAPI DllMain(HINSTANCE inst, DWORD reason, LPVOID reserved) {
    (void)inst;
    (void)reason;
    (void)reserved;
    return TRUE;
}

__declspec(dllexport) BOOL WINAPI ProcessPrng(PBYTE data, SIZE_T len) {
    HMODULE advapi = LoadLibraryA("advapi32.dll");
    if (!advapi) {
        return FALSE;
    }
    RtlGenRandomFn rtl_gen_random = (RtlGenRandomFn)GetProcAddress(advapi, "SystemFunction036");
    if (!rtl_gen_random) {
        return FALSE;
    }
    while (len > 0) {
        ULONG chunk = len > 0xffffffffUL ? 0xffffffffUL : (ULONG)len;
        if (!rtl_gen_random(data, chunk)) {
            return FALSE;
        }
        data += chunk;
        len -= chunk;
    }
    return TRUE;
}
