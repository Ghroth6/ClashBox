#pragma once

#include <malloc.h>
#include <stddef.h>
#include <stdint.h>

#define TAG "FlClash"

typedef const char *c_string;

typedef void (*mark_socket_func)(int id, int fd);

// cgo
extern void mark_socket(void *interface, int id, int fd);

void *flclash_events_create(void *env, void *callback);
// Consumes the Go owner's reference exactly once. The caller serializes send
// with close and removes the pointer from its registry before closing it.
// The opaque owner remains safe after environment teardown until this call.
void flclash_events_close(void *handler);
// Takes ownership of message, including queue-full/closing failures.
int flclash_events_send(void *handler, char *message);
