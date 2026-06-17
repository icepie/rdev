#include <windows.h>

static int same_bytes(volatile void *address, void *compare_address, SIZE_T address_size) {
    volatile unsigned char *left = (volatile unsigned char *)address;
    unsigned char *right = (unsigned char *)compare_address;

    for (SIZE_T i = 0; i < address_size; i++) {
        if (left[i] != right[i]) {
            return 0;
        }
    }

    return 1;
}

__declspec(dllexport) BOOL WINAPI WaitOnAddress(
    volatile VOID *Address,
    PVOID CompareAddress,
    SIZE_T AddressSize,
    DWORD dwMilliseconds
) {
    DWORD start = GetTickCount();

    while (same_bytes(Address, CompareAddress, AddressSize)) {
        if (dwMilliseconds == 0) {
            SetLastError(ERROR_TIMEOUT);
            return FALSE;
        }

        if (dwMilliseconds != INFINITE) {
            DWORD elapsed = GetTickCount() - start;
            if (elapsed >= dwMilliseconds) {
                SetLastError(ERROR_TIMEOUT);
                return FALSE;
            }
        }

        Sleep(1);
    }

    return TRUE;
}

__declspec(dllexport) VOID WINAPI WakeByAddressSingle(PVOID Address) {
    (void)Address;
}

__declspec(dllexport) VOID WINAPI WakeByAddressAll(PVOID Address) {
    (void)Address;
}
