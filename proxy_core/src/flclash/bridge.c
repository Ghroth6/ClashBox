#include "bridge.h"
#include <napi/native_api.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>


void mark_socket(void *interface, int id, int fd) {
    mark_socket_func func = (mark_socket_func)(interface);
    func(id, fd);
}

// The opaque owner outlives the runtime's TSFN. Go holds one reference until
// close; the finalizer and environment cleanup hook each hold their own. In
// particular, environment teardown cannot leave Go pointing at a freed TSFN.
typedef struct event_owner {
    pthread_mutex_t mutex;
    atomic_uint references;
    napi_threadsafe_function handler;
    bool cleanup_registered;
} event_owner;

static void event_owner_drop(event_owner *owner) {
    if (atomic_fetch_sub_explicit(&owner->references, 1, memory_order_acq_rel) == 1) {
        pthread_mutex_destroy(&owner->mutex);
        free(owner);
    }
}

static void event_abort(event_owner *owner) {
    pthread_mutex_lock(&owner->mutex);
    napi_threadsafe_function handler = owner->handler;
    owner->handler = NULL;
    // Keep teardown behind the same barrier as release. The recursive mutex
    // also permits a finalizer invoked inline on the environment thread.
    // Only the thread that detached the TSFN releases it.
    if (handler != NULL) {
        napi_status status = napi_release_threadsafe_function(handler, napi_tsfn_abort);
        if (status != napi_ok) {
            // A failed release must not cause a second release or premature
            // freeing of the finalizer's reference. Runtime teardown owns it.
            fprintf(stderr, "ClashBoxEvents: TSFN release failed (%d)\n", status);
        }
    }
    pthread_mutex_unlock(&owner->mutex);
}

static void event_env_cleanup(void *data) {
    event_owner *owner = data;
    pthread_mutex_lock(&owner->mutex);
    owner->cleanup_registered = false;
    pthread_mutex_unlock(&owner->mutex);
    event_abort(owner);
    event_owner_drop(owner); // cleanup hook reference
}

static void event_finalize(napi_env env, void *data, void *hint) {
    event_owner *owner = data;
    bool remove_cleanup = false;
    pthread_mutex_lock(&owner->mutex);
    owner->handler = NULL;
    // Finalization runs on the environment thread. Remove the hook after an
    // explicit close so repeated registrations do not retain owners until exit.
    // During environment teardown the hook may already have run, or env may be
    // NULL; in those cases its reference is released by the hook itself.
    if (env != NULL && owner->cleanup_registered &&
        napi_remove_env_cleanup_hook(env, event_env_cleanup, owner) == napi_ok) {
        owner->cleanup_registered = false;
        remove_cleanup = true;
    }
    pthread_mutex_unlock(&owner->mutex);
    if (remove_cleanup) event_owner_drop(owner);
    event_owner_drop(owner); // TSFN finalizer reference
}

static void event_call_js(napi_env env, napi_value callback, void *context, void *data) {
    char *message = data;
    if (env != NULL && callback != NULL) {
        napi_value args[2], receiver;
        if (napi_get_undefined(env, &receiver) == napi_ok &&
            napi_create_string_utf8(env, "", NAPI_AUTO_LENGTH, &args[0]) == napi_ok &&
            napi_create_string_utf8(env, message, NAPI_AUTO_LENGTH, &args[1]) == napi_ok) {
            napi_call_function(env, receiver, callback, 2, args, NULL);
        }
    }
    free(message);
}

void *flclash_events_create(void *raw_env, void *raw_callback) {
    napi_env env = raw_env;
    napi_value name;
    napi_valuetype callback_type;
    if (napi_typeof(env, raw_callback, &callback_type) != napi_ok || callback_type != napi_function) return NULL;
    if (napi_create_string_utf8(env, "ClashBoxEvents", NAPI_AUTO_LENGTH, &name) != napi_ok) return NULL;
    event_owner *owner = calloc(1, sizeof(*owner));
    if (owner == NULL) return NULL;
    pthread_mutexattr_t attr;
    if (pthread_mutexattr_init(&attr) != 0) { free(owner); return NULL; }
    int mutex_status = pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_RECURSIVE);
    if (mutex_status == 0) mutex_status = pthread_mutex_init(&owner->mutex, &attr);
    pthread_mutexattr_destroy(&attr);
    if (mutex_status != 0) { free(owner); return NULL; }
    atomic_init(&owner->references, 2); // Go ownership and TSFN finalizer
    if (napi_create_threadsafe_function(env, raw_callback, NULL, name, 128, 1,
            owner, event_finalize, owner, event_call_js, &owner->handler) != napi_ok) {
        event_owner_drop(owner);
        event_owner_drop(owner);
        return NULL;
    }
    atomic_fetch_add_explicit(&owner->references, 1, memory_order_relaxed);
    if (napi_add_env_cleanup_hook(env, event_env_cleanup, owner) != napi_ok) {
        event_owner_drop(owner); // hook was not installed
        event_abort(owner);
        event_owner_drop(owner); // no Go owner will be returned
        return NULL;
    }
    owner->cleanup_registered = true;
    if (napi_unref_threadsafe_function(env, owner->handler) != napi_ok) {
        event_abort(owner);
        event_owner_drop(owner);
        return NULL;
    }
    return owner;
}

void flclash_events_close(void *opaque) {
    if (opaque == NULL) return;
    event_owner *owner = opaque;
    event_abort(owner);
    event_owner_drop(owner); // exactly one Go ownership reference
}

int flclash_events_send(void *opaque, char *message) {
    if (opaque == NULL) { free(message); return napi_closing; }
    event_owner *owner = opaque;
    pthread_mutex_lock(&owner->mutex);
    napi_status status = owner->handler == NULL ? napi_closing :
        napi_call_threadsafe_function(owner->handler, message, napi_tsfn_nonblocking);
    pthread_mutex_unlock(&owner->mutex);
    if (status != napi_ok) free(message);
    return status;
}
