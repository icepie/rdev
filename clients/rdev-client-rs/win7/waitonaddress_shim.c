#include <windows.h>

BOOL WINAPI DllMain(HINSTANCE inst, DWORD reason, LPVOID reserved) {
    (void)inst;
    (void)reason;
    (void)reserved;
    return TRUE;
}

__declspec(dllexport) BOOL WINAPI WaitOnAddress(
    volatile VOID *address,
    PVOID compare_address,
    SIZE_T address_size,
    DWORD milliseconds) {
    DWORD start = GetTickCount();
    for (;;) {
        if (memcmp((const void *)address, compare_address, address_size) != 0) {
            return TRUE;
        }
        if (milliseconds != INFINITE && GetTickCount() - start >= milliseconds) {
            SetLastError(ERROR_TIMEOUT);
            return FALSE;
        }
        Sleep(1);
    }
}

__declspec(dllexport) VOID WINAPI WakeByAddressSingle(PVOID address) {
    (void)address;
}

__declspec(dllexport) VOID WINAPI WakeByAddressAll(PVOID address) {
    (void)address;
}
