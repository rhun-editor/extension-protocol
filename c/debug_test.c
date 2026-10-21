/* cc -std=c99 -Wall -Wextra -o /tmp/rhun-debug-c debug_test.c && /tmp/rhun-debug-c */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>

static int resolver(rx_ext *ext, const rx_debug_resolve_ask *req, rx_debug_config *out, rx_buf *why, void *user) {
    (void)ext;
    (void)user;
    const rx_debug_entry *p = rx_debug_config_get(&req->config, "program");
    if (p && !strcmp(p->value, "same")) return 0;
    if (p && !strcmp(p->value, "no")) {
        rx_buf_str(why, "no Go package\nhere");
        return -1;
    }
    if (p && !strcmp(p->value, "cancel")) return -2;
    assert(!strcmp(req->type, "go") && !strcmp(req->request, "launch") && req->no_debug && req->ctx.doc == 3);
    for (size_t i = 0; i < req->config.n; i++)
        rx_debug_config_set(out, req->config.entries[i].kind, req->config.entries[i].path, req->config.entries[i].value);
    rx_debug_config_set(out, 's', "mode", "test");
    return 1;
}

static void resolve_through_rx_answer(void) {
    rx_ext ext;
    memset(&ext, 0, sizeof ext);
    snprintf(ext.features, sizeof ext.features, "debug");
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.debug_resolve = resolver;
    const char *programs[] = {"same", "no", "cancel", "x"};
    for (int i = 0; i < 4; i++) {
        rx_debug_config c = {0};
        rx_debug_config_set(&c, 's', "type", "go");
        rx_debug_config_set(&c, 's', "program", programs[i]);
        rx_buf b = {NULL, 0, 0};
        rx_debug_config_encode(&c, &b);
        rx_buf_str(&b, "context 3 4 5 7\n/p/a.go");
        char len[32];
        snprintf(len, sizeof len, "%zu", b.len);
        char *f[] = {"9", "debug-resolve", "go", "launch", "1", len};
        rx_answer(&ext, &h, NULL, f, 6, (const unsigned char *)b.data, b.len);
        rx_debug_config_free(&c);
        rx_buf_free(&b);
    }
}

int main(void) {
    resolve_through_rx_answer();
    rx_debug_config c = {0};
    assert(rx_debug_config_set(&c, 's', "type", "go") == 0);
    assert(rx_debug_config_set(&c, 's', "args.0", "-test.run") == 0);
    assert(rx_debug_config_set(&c, 's', "args.1", "^TestAdd$") == 0);
    assert(rx_debug_config_set(&c, 'n', "count", "-7") == 0);
    assert(rx_debug_config_set(&c, 'b', "stopOnEntry", "true") == 0);
    assert(rx_debug_config_set(&c, 'z', "extra", NULL) == 0);
    assert(rx_debug_config_set(&c, 'b', "bad", "yes") == -1);
    assert(rx_debug_config_set(&c, 'n', "bad", "1x") == -1);
    rx_buf b = {NULL, 0, 0};
    assert(rx_debug_config_encode(&c, &b) == 0);
    rx_buf_str(&b, "context 3 4 5 6\n/a/b.gounknown 1 2 3\nxyz");
    rx_debug_config got;
    rx_debug_context ctx = {0};
    assert(rx_debug_config_parse((const unsigned char *)b.data, b.len, &got, &ctx) == 0);
    assert(got.n == 6 && !strcmp(got.entries[2].value, "^TestAdd$") && got.entries[3].kind == 'n');
    assert(ctx.doc == 3 && ctx.at.line == 4 && ctx.at.col == 5 && ctx.path.len == 6 && !memcmp(ctx.path.data, "/a/b.g", 6));
    rx_debug_config_remove(&got, "args");
    assert(got.n == 4 && rx_debug_config_get(&got, "args.0") == NULL && rx_debug_config_get(&got, "type"));
    const char *bad[] = {"1\nx 1 1\nab", "1\nn 1 2\na1x", "1\nb 1 3\nayes", "1\nz 1 1\naa", "2\ns 1 1\nab", "x"};
    for (size_t i = 0; i < sizeof bad / sizeof *bad; i++) {
        rx_debug_config t;
        assert(rx_debug_config_parse((const unsigned char *)bad[i], strlen(bad[i]), &t, NULL) == -1);
    }
    char *argv[] = {"7", "3"};
    rx_event ev = {"debug-ended", 2, argv, "7 3", NULL, 0};
    int64_t s;
    int code, has;
    assert(rx_parse_debug_ended(&ev, &s, &code, &has) == 1 && s == 7 && code == 3 && has);
    argv[1] = "-";
    assert(rx_parse_debug_ended(&ev, &s, &code, &has) == 1 && !has);
    rx_debug_config_free(&c);
    rx_debug_config_free(&got);
    rx_buf_free(&b);
    rx_buf_free(&ctx.path);
    puts("ok");
    return 0;
}
