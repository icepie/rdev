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

int WSAAPI WSACleanup(void) {
    typedef int (WSAAPI *Fn)(void);
    Fn fn = (Fn)ws2_proc("WSACleanup");
    if (!fn) {
        WSASetLastError(WSAEOPNOTSUPP);
        return SOCKET_ERROR;
    }
    return fn();
}

void WSAAPI freeaddrinfo(PADDRINFOA ai) {
    typedef void (WSAAPI *Fn)(PADDRINFOA);
    Fn fn = (Fn)ws2_proc("freeaddrinfo");
    if (fn) {
        fn(ai);
    }
}

int WSAAPI getaddrinfo(PCSTR node, PCSTR service, const ADDRINFOA *hints, PADDRINFOA *result) {
    typedef int (WSAAPI *Fn)(PCSTR, PCSTR, const ADDRINFOA *, PADDRINFOA *);
    Fn fn = (Fn)ws2_proc("getaddrinfo");
    if (!fn) {
        return WSAEOPNOTSUPP;
    }
    return fn(node, service, hints, result);
}

int WSAAPI select(int nfds, fd_set *readfds, fd_set *writefds, fd_set *exceptfds, const struct timeval *timeout) {
    typedef int (WSAAPI *Fn)(int, fd_set *, fd_set *, fd_set *, const struct timeval *);
    Fn fn = (Fn)ws2_proc("select");
    if (!fn) {
        WSASetLastError(WSAEOPNOTSUPP);
        return SOCKET_ERROR;
    }
    return fn(nfds, readfds, writefds, exceptfds, timeout);
}
