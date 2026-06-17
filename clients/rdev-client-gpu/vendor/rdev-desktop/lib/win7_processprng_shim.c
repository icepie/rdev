#include <windows.h>

BOOLEAN NTAPI SystemFunction036(PVOID RandomBuffer, ULONG RandomBufferLength);

__declspec(dllexport) BOOL WINAPI ProcessPrng(PBYTE pbData, SIZE_T cbData) {
    if (cbData > 0xffffffffu) {
        SetLastError(ERROR_INVALID_PARAMETER);
        return FALSE;
    }

    if (SystemFunction036(pbData, (ULONG)cbData)) {
        return TRUE;
    }

    SetLastError(ERROR_GEN_FAILURE);
    return FALSE;
}
