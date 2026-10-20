/* cc -std=c99 -Wall -Wextra -Werror -o /tmp/rhun-tasks-c tasks_test.c && /tmp/rhun-tasks-c (from c/) */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>

/* what rhun's print-pattern prints for a pattern and a line */
static void result(const char *p, const char *line, char *out, size_t cap) {
    const char *why = rx_check_pattern(p, strlen(p));
    size_t caps[8][2];
    unsigned has;
    static const char *const names[] = {"file", "line", "col", "endline", "endcol", "severity", "code", "message"};
    if (why) {
        snprintf(out, cap, "error=%s", why);
        return;
    }
    if (!rx_match_pattern(p, strlen(p), line, strlen(line), caps, &has)) {
        snprintf(out, cap, "match=0");
        return;
    }
    size_t n = (size_t)snprintf(out, cap, "match=1");
    for (int k = 0; k < 8; k++)
        if (has & (1u << k)) n += (size_t)snprintf(out + n, cap - n, " %s=%.*s", names[k], (int)caps[k][1], line + caps[k][0]);
}

int main(void) {
    FILE *f = fopen("../v1.1/patterns.txt", "rb");
    assert(f);
    char row[2048], got[2048];
    int n = 0;
    while (fgets(row, sizeof row, f)) {
        row[strcspn(row, "\n")] = 0;
        if (row[0] == '#' || !row[0]) continue;
        char *t1 = strchr(row, '\t'), *t2 = strrchr(row, '\t');
        assert(t1 && t2 && t1 != t2);
        *t1 = 0, *t2 = 0;
        result(row, t1 + 1, got, sizeof got);
        if (strcmp(got, t2 + 1)) {
            fprintf(stderr, "%s on %s: %s, want %s\n", row, t1 + 1, got, t2 + 1);
            return 1;
        }
        n++;
    }
    fclose(f);
    assert(n >= 30);

    static const char *const names[] = {"go", "gcc", "rustc", "tsc", "msbuild"};
    for (int i = 0; i < 5; i++) assert(!rx_check_matcher(rx_builtin_matcher(names[i]), strlen(rx_builtin_matcher(names[i]))));
    assert(!strcmp(rx_check_matcher("2 {file}: {message}", 19), "steps go 1, 2, 3, 4 in order"));
    assert(!strcmp(rx_check_matcher("loop", 4), "a matcher needs a step 1 pattern"));

    /* the design's example, byte for byte */
    const char *go[] = {"go", NULL};
    rx_task t = {0};
    t.id = "build", t.label = "Go build", t.program.shell = "go build ./...", t.group = "build", t.is_default = 1;
    t.builtin_matchers = go;
    rx_buf b = {NULL, 0, 0};
    const char *why;
    assert(rx_task_encode(&t, &b, &why) == 0);
    const char *want = "build shell 8 14 0 37\nGo buildgo build ./...group build 0\ndefault 0\nmatcher go 0\n";
    assert(b.len == strlen(want) && !memcmp(b.data, want, b.len));
    rx_buf_free(&b);
    const char *argv[] = {"python3", "-m", "http.server", NULL}, *ready[] = {"Serving HTTP on {_}", NULL}, *bad[] = {"{x}", NULL};
    rx_task s = {0};
    s.id = "serve", s.label = "Serve", s.program.argv = argv, s.ready = ready;
    assert(rx_task_encode(&s, &b, &why) == 0);
    want = "serve argv 5 23 0 28\nServepython3\0-m\0http.server\0ready 19\nServing HTTP on {_}";
    assert(b.len == 21 + 5 + 23 + 28 && !memcmp(b.data, want, b.len));
    rx_buf_free(&b);
    s.ready = bad;
    assert(rx_task_encode(&s, &b, &why) == -1 && !strcmp(why, "unknown field"));
    s.ready = NULL, s.busy = ready;
    assert(rx_task_encode(&s, &b, &why) == -1 && !strcmp(why, "busy needs ready"));
    rx_terminal_spec spec = {0};
    spec.label = "Shell";
    assert(rx_terminal_encode(&spec, &b, &why) == 0 && b.len == 21 && !memcmp(b.data, "- shell 5 0 0 0\nShell", 21));
    rx_buf_free(&b);

    char *f1[] = {"3", "2", "6"};
    rx_event ev = {0};
    ev.name = "terminal-output", ev.argc = 3, ev.argv = f1, ev.body = (const unsigned char *)"ab\ncd\n", ev.body_len = 6;
    rx_terminal_event te;
    assert(rx_parse_terminal_event(&ev, &te) && !strcmp(te.kind, "output") && te.term == 3 && te.dropped == 2 && te.lines_len == 6);
    char *f2[] = {"4", "failed", "-", "0", "0"};
    ev.name = "terminal-ended", ev.argc = 5, ev.argv = f2;
    assert(rx_parse_terminal_event(&ev, &te) && te.code == -1 && !strcmp(te.reason, "failed"));
    char *f3[] = {"4", "81234", "build"};
    ev.name = "terminal-started", ev.argc = 3, ev.argv = f3;
    assert(rx_parse_terminal_event(&ev, &te) && te.pid == 81234 && !strcmp(te.task, "build"));
    char *f4[] = {"0", "1", "-"};
    ev.argv = f4;
    assert(!rx_parse_terminal_event(&ev, &te));
    rx_ext ext = {0};
    assert(rx_start_task(&ext, "build") == 0 && strstr(rx_error(&ext), "tasks was not agreed"));
    puts("ok");
    return 0;
}
