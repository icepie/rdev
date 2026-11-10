#ifndef RDEV_CUA_BRIDGE_H
#define RDEV_CUA_BRIDGE_H

#include <stdbool.h>
#ifdef __cplusplus
extern "C" {
#endif

void *rdev_cua_session_new(void);
char *rdev_cua_session_request(void *session, const char *request_json);

bool rdev_cua_supported(void);
void rdev_cua_session_free(void *session);
void rdev_cua_string_free(char *value);

#ifdef __cplusplus
}
#endif

#endif
