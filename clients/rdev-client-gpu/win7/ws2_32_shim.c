#define WINSOCK_API_LINKAGE
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>

static HMODULE real_ws2(void) {
    static HMODULE module = NULL;
    if (!module) {
        module = LoadLibraryA("ws2_32.dll");
    }
    return module;
}

static FARPROC ws2_proc(const char *name) {
    HMODULE module = real_ws2();
    if (!module) {
        return NULL;
    }
    return GetProcAddress(module, name);
}

int WSAAPI GetHostNameW(PWSTR name, int namelen) {
    typedef int (WSAAPI *GetHostNameWFn)(PWSTR, int);
    GetHostNameWFn get_host_name_w = (GetHostNameWFn)ws2_proc("GetHostNameW");
    if (get_host_name_w) {
        return get_host_name_w(name, namelen);
    }

    typedef int (WSAAPI *GetHostNameAFn)(char *, int);
    GetHostNameAFn get_host_name_a = (GetHostNameAFn)ws2_proc("gethostname");
    if (!get_host_name_a) {
        WSASetLastError(WSAEOPNOTSUPP);
        return SOCKET_ERROR;
    }

    char ansi_name[256];
    if (get_host_name_a(ansi_name, sizeof(ansi_name)) != 0) {
        return SOCKET_ERROR;
    }
    if (MultiByteToWideChar(CP_ACP, 0, ansi_name, -1, name, namelen) == 0) {
        WSASetLastError(WSAEINVAL);
        return SOCKET_ERROR;
    }
    return 0;
}
