/* cc -std=c99 -Wall -Wextra -o /tmp/rhun-commands-c commands_test.c && /tmp/rhun-commands-c */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>

static rx_buf seen;

static void on_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    rx_buf res = {NULL, 0, 0};
    assert(rx_run_command(ext, "bob/tools.format", "x y", 3, &res) == 0 && res.len == 4 && !memcmp(res.data, "done", 4));
    assert(rx_run_command(ext, "demo.self", NULL, 0, NULL) == -1 && !strcmp(rx_error(ext), "refused its own command"));
    assert(rx_run_command(ext, "a b", NULL, 0, NULL) == -1 && !strncmp(rx_error(ext), "syntax", 6));
    assert(rx_set_context(ext, "mode", "insert here") == 0);
    assert(rx_remove_context(ext, "mode") == 0);
    assert(rx_set_context(ext, "Mode", "x") == -1 && rx_set_context(ext, "a", "x\ty") == -1 &&
           rx_set_context(ext, "a", "-") == -1 && rx_set_context(ext, "a", "") == -1);
    rx_status_item it = {0};
    it.name = "go";
    it.text = "Go";
    assert(rx_set_status_item(ext, &it) == 0);
    assert(rx_remove_status_item(ext, "go") == 0);
    rx_buf_free(&res);
}

static void on_invoke(rx_ext *ext, const rx_invocation *inv, void *user) {
    (void)ext, (void)user;
    rx_buf_fmt(&seen, "run %s %s %lld %s [%.*s]", inv->command, inv->name, (long long)inv->doc, inv->from,
               (int)inv->body_len, (const char *)inv->body);
    const char *t;
    const unsigned char *a;
    size_t tl, al;
    if (rx_menu_target(inv, &t, &tl, &a, &al) == 0) rx_buf_fmt(&seen, " target=%.*s args=%.*s", (int)tl, t, (int)al, (const char *)a);
    rx_buf_str(&seen, "\n");
}

static int on_call(rx_ext *ext, const rx_call_ask *req, rx_buf *out, const char **code, void *user) {
    (void)ext, (void)user;
    if (req->args_len == 5 && !memcmp(req->args, "nodoc", 5)) {
        *code = "nodoc";
        rx_buf_str(out, "no document");
        return -1;
    }
    if (req->args_len == 3 && !memcmp(req->args, "BAD", 3)) {
        *code = "Bad Code";
        rx_buf_str(out, "why\nnot");
        return -1;
    }
    rx_buf_fmt(out, "got %.*s from %s %s %lld", (int)req->args_len, (const char *)req->args, req->caller, req->name,
               (long long)req->doc);
    return 1;
}

static int run_bytes(const char *features, const rx_handlers *h, const char *input, size_t n, rx_buf *got) {
    FILE *in = fopen("rhunext-commands.in", "wb");
    assert(in != NULL);
    assert(fwrite(input, 1, n, in) == n);
    fclose(in);
    FILE *r = freopen("rhunext-commands.in", "rb", stdin), *w = freopen("rhunext-commands.out", "wb", stdout);
    assert(r != NULL && w != NULL);
    int code = rx_run(features, h, NULL);
    fflush(stdout);
    FILE *out = fopen("rhunext-commands.out", "rb");
    assert(out != NULL);
    char chunk[4096];
    size_t k;
    while ((k = fread(chunk, 1, sizeof chunk, out)) > 0) rx_buf_add(got, chunk, k);
    fclose(out);
    return code;
}

static void check(const rx_buf *got, const char *want) {
    if (got->len != strlen(want) || memcmp(got->data, want, got->len) != 0) {
        fprintf(stderr, "got:\n%.*s\nwant:\n%s\n", (int)got->len, got->data, want);
        assert(0);
    }
}

/* the shared when-clause vectors (v1.1/when-vectors.txt) */
static void when_vectors(void) {
    FILE *f = fopen("../v1.1/when-vectors.txt", "rb");
    if (!f) f = fopen("v1.1/when-vectors.txt", "rb");
    assert(f != NULL);
    char line[1024];
    int n = 0;
    while (fgets(line, sizeof line, f)) {
        size_t l = strlen(line);
        while (l && (line[l - 1] == '\n' || line[l - 1] == '\r')) line[--l] = 0;
        if (!l || line[0] == '#') continue;
        char *tab = strchr(line, '\t');
        assert(tab != NULL);
        *tab = 0;
        const char *clause = tab + 1;
        size_t at = 0;
        const char *msg = NULL;
        int r = rx_when_check(clause, strlen(clause), &at, &msg);
        if (strncmp(line, "error ", 6) == 0) {
            if (r != -1 || at != (size_t)atoi(line + 6)) {
                fprintf(stderr, "when %s: %d at %zu (%s)\n", clause, r, at, msg ? msg : "");
                assert(0);
            }
        } else if (r != 0) {
            fprintf(stderr, "when %s: %s at %zu\n", clause, msg, at);
            assert(0);
        }
        n++;
    }
    fclose(f);
    assert(n >= 50);
}

int main(void) {
    when_vectors();
    rx_buf b = {NULL, 0, 0};
    assert(rx_command_ref(&b, "run_task", "all\nx", 5) == 0);
    check(&b, "command run_task 5\nall\nx");
    assert(rx_command_ref(&b, "Run", "", 0) == -1 && rx_command_ref(&b, "", "", 0) == -1 &&
           rx_command_ref(&b, "a", "", 8193) == -1);
    rx_buf_free(&b);
    rx_status_item it = {0};
    it.name = "build", it.align = "left", it.priority = 30, it.text = "$(gear) Build", it.tooltip = "Builds it";
    it.command = "run_task", it.args = "all", it.args_len = 3, it.color = "accent", it.background = "error";
    it.when = "projectOpen";
    assert(rx_status_item_encode(&it, &b) == 0);
    check(&b, "left 30 13 9 75\n$(gear) BuildBuilds itcommand run_task 3\nallcolor accent 0\nbackground error 0\nwhen 11\nprojectOpen");
    rx_buf_free(&b);
    it.color = "pink";
    assert(rx_status_item_encode(&it, &b) == -1 && b.len == 0);
    it.color = NULL, it.priority = 1001;
    assert(rx_status_item_encode(&it, &b) == -1);
    assert(rx_plain_text("\xc3\xa9 ok", 5) && !rx_plain_text("a\tb", 3) && !rx_plain_text("\xe2\x80\xae", 3) &&
           !rx_plain_text("\xc3", 1) && !rx_plain_text("\xc0\x80", 2));

    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_ready;
    h.invoke = on_invoke;
    h.call = on_call;
    const char *in =
        "0 features command-args context-keys status-items\n0 ready -\n"
        "ok 1 4\ndone"
        "error 2 refused its own command\n"
        "ok 3\nok 4\nok 5\nok 6\n"
        "0 run demo.go 3 key 4\nfast"
        "0 run demo.copy 2 menu 13\nroot /p\nextra"
        "0 run demo.old 5\n"
        "7 call demo.fmt 4 bob/tools 3\nabc"
        "8 call demo.fmt 0 bob/tools 5\nnodoc"
        "9 call demo.fmt 0 bob/tools 3\nBAD"
        "0 bye\n";
    rx_buf got = {NULL, 0, 0};
    assert(run_bytes("command-args context-keys status-items", &h, in, strlen(in), &got) == 0);
    check(&got,
          "hello 1 command-args context-keys status-items\n"
          "1 run-command bob/tools.format 3\nx y"
          "2 run-command demo.self 0\n"
          "3 set-context mode insert here\n"
          "4 set-context mode -\n"
          "5 status-item go 16\nright 0 2 0 0\nGo"
          "6 status-remove go\n"
          "ok 7 28\ngot abc from bob/tools fmt 4"
          "error 8 nodoc no document\n"
          "error 9 refused why not\n");
    check(&seen,
          "run demo.go go 3 key [fast]\n"
          "run demo.copy copy 2 menu [root /p\nextra] target=root /p args=extra\n"
          "run demo.old old 5  []\n");
    rx_buf_free(&got);
    rx_buf_free(&seen);

    /* without the feature a call is unknown, and a v1 command handler still gets runs */
    memset(&h, 0, sizeof h);
    h.call = on_call;
    in = "0 ready -\n4 call demo.x 0 bob/t 2\nhi0 bye\n";
    assert(run_bytes("", &h, in, strlen(in), &got) == 0);
    check(&got, "hello 1\nerror 4 unknown no such request\n");
    rx_buf_free(&got);
    remove("rhunext-commands.in");
    remove("rhunext-commands.out");
    return 0;
}
