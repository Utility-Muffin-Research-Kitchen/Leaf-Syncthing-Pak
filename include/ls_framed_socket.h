#ifndef LS_FRAMED_SOCKET_H
#define LS_FRAMED_SOCKET_H

#include <stddef.h>

#define LS_FRAME_MAX (64u * 1024u)

int ls_frame_request(const char *socket_path,
                     const char *request,
                     size_t request_len,
                     char **response,
                     size_t *response_len,
                     int timeout_ms);

#endif
