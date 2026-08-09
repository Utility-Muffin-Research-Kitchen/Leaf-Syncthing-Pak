#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include "ls_framed_socket.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <unistd.h>

static int ls_io_all(int fd, void *buffer, size_t length, int writing) {
    unsigned char *cursor = buffer;
    while (length > 0) {
        ssize_t count = writing ? write(fd, cursor, length) : read(fd, cursor, length);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) return -1;
        cursor += (size_t)count;
        length -= (size_t)count;
    }
    return 0;
}

int ls_frame_request(const char *socket_path,
                     const char *request,
                     size_t request_len,
                     char **response,
                     size_t *response_len,
                     int timeout_ms) {
    struct sockaddr_un address;
    struct timeval timeout;
    uint32_t network_length;
    uint32_t received_length;
    char *payload = NULL;
    int fd = -1;
    int flags;

    if (!socket_path || !request || request_len == 0 || request_len > LS_FRAME_MAX ||
        !response || !response_len || timeout_ms <= 0 ||
        strlen(socket_path) >= sizeof(address.sun_path)) return -1;
    *response = NULL;
    *response_len = 0;

    fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return -1;
    flags = fcntl(fd, F_GETFD);
    if (flags >= 0) (void)fcntl(fd, F_SETFD, flags | FD_CLOEXEC);
    timeout.tv_sec = timeout_ms / 1000;
    timeout.tv_usec = (timeout_ms % 1000) * 1000;
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout));
    (void)setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout));

    memset(&address, 0, sizeof(address));
    address.sun_family = AF_UNIX;
    memcpy(address.sun_path, socket_path, strlen(socket_path) + 1u);
    if (connect(fd, (struct sockaddr *)&address, sizeof(address)) != 0) goto fail;

    network_length = htonl((uint32_t)request_len);
    if (ls_io_all(fd, &network_length, sizeof(network_length), 1) != 0 ||
        ls_io_all(fd, (void *)request, request_len, 1) != 0 ||
        ls_io_all(fd, &received_length, sizeof(received_length), 0) != 0) goto fail;
    received_length = ntohl(received_length);
    if (received_length == 0 || received_length > LS_FRAME_MAX) goto fail;
    payload = malloc((size_t)received_length + 1u);
    if (!payload || ls_io_all(fd, payload, received_length, 0) != 0 ||
        memchr(payload, '\0', received_length) != NULL) goto fail;
    payload[received_length] = '\0';
    close(fd);
    *response = payload;
    *response_len = received_length;
    return 0;

fail:
    free(payload);
    if (fd >= 0) close(fd);
    return -1;
}
