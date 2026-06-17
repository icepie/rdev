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

int WSAAPI WSAStartup(WORD version, LPWSADATA data) {
    typedef int (WSAAPI *Fn)(WORD, LPWSADATA);
    Fn fn = (Fn)ws2_proc("WSAStartup");
    if (!fn) {
        return WSAEOPNOTSUPP;
    }
    return fn(version, data);
}

int WSAAPI WSAGetLastError(void) {
    typedef int (WSAAPI *Fn)(void);
    Fn fn = (Fn)ws2_proc("WSAGetLastError");
    return fn ? fn() : WSAEOPNOTSUPP;
}

void WSAAPI WSASetLastError(int error) {
    typedef void (WSAAPI *Fn)(int);
    Fn fn = (Fn)ws2_proc("WSASetLastError");
    if (fn) {
        fn(error);
    }
}

int WSAAPI __WSAFDIsSet(SOCKET fd, fd_set *set) {
    typedef int (WSAAPI *Fn)(SOCKET, fd_set *);
    Fn fn = (Fn)ws2_proc("__WSAFDIsSet");
    if (!fn) {
        WSASetLastError(WSAEOPNOTSUPP);
        return 0;
    }
    return fn(fd, set);
}

int WSAAPI gethostname(char *name, int namelen) {
    typedef int (WSAAPI *Fn)(char *, int);
    Fn fn = (Fn)ws2_proc("gethostname");
    if (!fn) {
        WSASetLastError(WSAEOPNOTSUPP);
        return SOCKET_ERROR;
    }
    return fn(name, namelen);
}

int WSAAPI getnameinfo(const SOCKADDR *addr, socklen_t addrlen, PCHAR host, DWORD hostlen, PCHAR serv, DWORD servlen, INT flags) {
    typedef int (WSAAPI *Fn)(const SOCKADDR *, socklen_t, PCHAR, DWORD, PCHAR, DWORD, INT);
    Fn fn = (Fn)ws2_proc("getnameinfo");
    if (!fn) {
        return WSAEOPNOTSUPP;
    }
    return fn(addr, addrlen, host, hostlen, serv, servlen, flags);
}

u_short WSAAPI htons(u_short hostshort) {
    typedef u_short (WSAAPI *Fn)(u_short);
    Fn fn = (Fn)ws2_proc("htons");
    return fn ? fn(hostshort) : hostshort;
}

u_long WSAAPI ntohl(u_long netlong) {
    typedef u_long (WSAAPI *Fn)(u_long);
    Fn fn = (Fn)ws2_proc("ntohl");
    return fn ? fn(netlong) : netlong;
}

u_short WSAAPI ntohs(u_short netshort) {
    typedef u_short (WSAAPI *Fn)(u_short);
    Fn fn = (Fn)ws2_proc("ntohs");
    return fn ? fn(netshort) : netshort;
}

SOCKET WSAAPI socket(int af, int type, int protocol) {
    typedef SOCKET (WSAAPI *Fn)(int, int, int);
    Fn fn = (Fn)ws2_proc("socket");
    if (!fn) {
        WSASetLastError(WSAEOPNOTSUPP);
        return INVALID_SOCKET;
    }
    return fn(af, type, protocol);
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
