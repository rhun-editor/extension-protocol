/* tests for rhunext.h: cc -std=c99 -Wall -Wextra -o test test.c && ./test */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>
#include <locale.h>
#include <stdio.h>

static rx_range R(int l1, int c1, int l2, int c2) {
    rx_range r;
    r.start.line = l1, r.start.col = c1, r.end.line = l2, r.end.col = c2;
    return r;
}

/* b holds exactly the bytes of want */
static void same(const rx_buf *b, const char *want) {
    size_t n = strlen(want);
    if (b->len != n || (n && memcmp(b->data, want, n) != 0)) {
        fprintf(stderr, "got %zu bytes:\n[%.*s]\nwant %zu bytes:\n[%s]\n", b->len, (int)b->len, b->data ? b->data : "", n, want);
        abort();
    }
}

static void old_tests(void) {
    rx_buf p = {NULL, 0, 0};
    setlocale(LC_NUMERIC, "de_DE.UTF-8");
    rx_rect(&p, 1.5, -0.004, 12.25, 7.257, "accent");
    rx_rect(&p, -2.5, 1e12, 0.1, -3, "fg");
    printf("%s", p.data);
    assert(strcmp(p.data, "rect 1.5 0 12.25 7.26 accent\nrect -2.5 9999999 0.1 0 fg\n") == 0);
    rx_buf b = {NULL, 0, 0};
    for (int i = 0; i < 200; i++) rx_buf_add(&b, "x", 1);
    rx_buf_add(&b, b.data, b.len);
    assert(b.len == 400);
    assert(rx_atof("-12.5") == -12.5 && rx_atof("7") == 7);
    rx_buf s = {NULL, 0, 0};
    char *doc = (char *)malloc(70001); memset(doc, 'a', 70000); doc[69990] = (char)0xc3; doc[69991] = (char)0xa9; doc[70000] = 0;
    rx_signature_answer(&s, "f(x)", 2, 3, doc);
    assert(s.len <= 65536);
    rx_buf s2 = {NULL, 0, 0};
    char *sig = (char *)malloc(70001); memset(sig, 'x', 70000); sig[70000] = 0;
    rx_signature_answer(&s2, sig, -1, -1, "d");
    assert(s2.len == 0);
    rx_buf_free(&p), rx_buf_free(&b), rx_buf_free(&s), rx_buf_free(&s2);
    free(doc), free(sig);
}

static void locations(void) {
    rx_locations l = {0};
    rx_buf b = {NULL, 0, 0};
    rx_location(&l, "/a/b.rs", 0, R(1, 4, 1, 8), "  fn main() {\n");
    rx_location(&l, NULL, 7, R(2, 1, 2, 1), NULL);
    rx_location(&l, "", 8, R(3, 1, 3, 2), "\ta\r\nb ");     /* "" is no path; a line break inside is a space */
    rx_locations_finish(&l, &b);
    same(&b, "3\n1 4 1 8 7 11\n/a/b.rsfn main() {2 1 2 1 2 0\n#73 1 3 2 2 4\n#8a  b");
    assert(l.n == 0 && l.recs.data == NULL);
    rx_buf_clear(&b);
    rx_locations_finish(&l, &b);
    same(&b, "0\n");

    /* 10000 records at most */
    for (int i = 0; i < 10001; i++) rx_location(&l, NULL, 1, R(1, 1, 1, 1), NULL);
    assert(l.n == 10000 && l.full);
    rx_buf_clear(&b);
    rx_locations_finish(&l, &b);
    assert(b.len > 6 && memcmp(b.data, "10000\n", 6) == 0);

    /* 1 MB at most: the record that does not fit, and every later one, are left out */
    char *pre = (char *)malloc(1001);
    memset(pre, 'p', 1000), pre[1000] = 0;
    size_t rec = strlen("1 1 1 1 2 1000\n") + 2 + 1000;
    int fit = (int)(((size_t)1 << 20) - 16) / (int)rec;
    for (int i = 0; i < fit + 1; i++) rx_location(&l, NULL, 1, R(1, 1, 1, 1), pre);
    rx_location(&l, NULL, 1, R(1, 1, 1, 1), NULL);
    assert(l.n == fit && l.full);
    rx_buf_clear(&b);
    rx_locations_finish(&l, &b);
    char head[32];
    snprintf(head, sizeof head, "%d\n", fit);
    assert(b.len == strlen(head) + (size_t)fit * rec && b.len <= ((size_t)1 << 20));
    assert(memcmp(b.data, head, strlen(head)) == 0);
    free(pre);
    rx_buf_free(&b);
}

static void symbols(void) {
    rx_symbols l = {0};
    rx_buf b = {NULL, 0, 0};
    rx_symbol(&l, R(1, 1, 3, 2), "struct", 0, "Point", NULL);
    rx_symbol(&l, R(2, 5, 2, 11), "field", 1, "x", "i32");
    rx_symbols_finish(&l, &b);
    same(&b, "2\n1 1 3 2 struct 0 5 0\nPoint2 5 2 11 field 1 1 3\nxi32");
    rx_buf_clear(&b);
    /* no kind is function; a kind is one word; a depth is not negative; name and detail one line;
     * a symbol without a name is left out */
    rx_symbol(&l, R(1, 1, 1, 1), NULL, -2, "a\nb", "c\rd");
    rx_symbol(&l, R(1, 1, 1, 1), " enum member ", 0, "e", NULL);
    rx_symbol(&l, R(1, 1, 1, 1), "variable", 0, NULL, "no name");
    rx_symbol(&l, R(1, 1, 1, 1), "variable", 0, "", NULL);
    rx_symbols_finish(&l, &b);
    same(&b, "2\n1 1 1 1 function 0 3 3\na bc d1 1 1 1 enummember 0 1 0\ne");
    for (int i = 0; i < 10001; i++) rx_symbol(&l, R(1, 1, 1, 1), "variable", 0, "v", NULL);
    assert(l.n == 10000);
    rx_buf_clear(&b);
    rx_symbols_finish(&l, &b);
    assert(memcmp(b.data, "10000\n", 6) == 0);
    rx_buf_free(&b);
}

static void wedits(void) {
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0};
    rx_wedit_file(&e, NULL, 3, 9);
    rx_wedit_edit(&e, R(1, 1, 1, 4), "new", 3);
    rx_wedit_file(&e, "/p/x.rs", 0, RX_ON_DISK);
    rx_wedit_edit(&e, R(2, 1, 2, 1), "a\n", 2);
    rx_wedit_edit(&e, R(5, 1, 5, 3), "", 0);
    rx_wedit_file(&e, "/p/y.rs", 0, 0);              /* a file without edits */
    assert(rx_wedit_finish(&e, &b) == 0 && !e.fail);
    same(&b, "3\n2 9 1\n#31 1 1 4 3\nnew7 - 2\n/p/x.rs2 1 2 1 2\na\n5 1 5 3 0\n7 0 0\n/p/y.rs");
    assert(e.body.data == NULL && e.group.data == NULL && e.target.data == NULL && e.files == 0);
    rx_buf_clear(&b);
    assert(rx_wedit_finish(&e, &b) == 0);             /* the empty edit, and a finished one is empty again */
    same(&b, "0\n");

    /* 100000 edits, 1000 files at most */
    rx_buf_clear(&b);
    rx_wedit_file(&e, NULL, 1, 1);
    for (int i = 0; i < 100000; i++) rx_wedit_edit(&e, R(1, 1, 1, 1), "", 0);
    assert(rx_wedit_finish(&e, &b) == 0 && b.len > 0);
    rx_buf_clear(&b);
    rx_wedit_file(&e, NULL, 1, 1);
    for (int i = 0; i < 100001; i++) rx_wedit_edit(&e, R(1, 1, 1, 1), "", 0);
    assert(rx_wedit_finish(&e, &b) == -1 && b.len == 0);
    assert(strcmp(e.fail, "toobig") == 0 && strcmp(e.why, "more than 100000 edits") == 0);
    rx_wedit_free(&e);
    assert(!e.fail && !e.why[0]);
    for (int i = 0; i < 1000; i++) rx_wedit_file(&e, NULL, i, 1);
    assert(rx_wedit_finish(&e, &b) == 0 && memcmp(b.data, "1000\n", 5) == 0);
    rx_buf_clear(&b);
    for (int i = 0; i < 1001; i++) rx_wedit_file(&e, NULL, i, 1);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);          /* after a failure: nothing */
    assert(rx_wedit_finish(&e, &b) == -1 && b.len == 0 && strcmp(e.fail, "toobig") == 0);
    rx_wedit_free(&e);

    /* 4 MB at most, the whole body counted: 2 + 6 + 2 + 16 bytes of headers and the text */
    size_t big = ((size_t)4 << 20) - strlen("1\n2 1 1\n#11 1 1 1 4194278\n");
    char *text = (char *)malloc(big + 2);
    memset(text, 'x', big + 1);
    rx_wedit_file(&e, NULL, 1, 1);
    rx_wedit_edit(&e, R(1, 1, 1, 1), text, big);
    assert(rx_wedit_finish(&e, &b) == 0 && b.len == ((size_t)4 << 20));
    rx_buf_clear(&b);
    rx_wedit_file(&e, NULL, 1, 1);
    rx_wedit_edit(&e, R(1, 1, 1, 1), text, big + 1);
    assert(rx_wedit_finish(&e, &b) == -1 && b.len == 0 && strcmp(e.why, "over 4194304 bytes") == 0);
    rx_wedit_free(&e);
    free(text);

    /* an edit needs its file */
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    assert(rx_wedit_finish(&e, &b) == -1 && strcmp(e.fail, "syntax") == 0);
    rx_wedit_free(&e);

    /* refused: the reason is one line, cut at a character boundary, and wins over a limit */
    rx_wedit_file(&e, NULL, 1, 1);
    assert(rx_refuse(&e, "not a name\nhere") == -1);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    assert(rx_wedit_finish(&e, &b) == -1 && b.len == 0);
    assert(strcmp(e.fail, "refused") == 0 && strcmp(e.why, "not a name here") == 0);
    rx_wedit_free(&e);
    for (int i = 0; i < 1001; i++) rx_wedit_file(&e, NULL, i, 1);
    char long_why[601];
    for (int i = 0; i < 600; i += 2) long_why[i] = (char)0xc3, long_why[i + 1] = (char)0xa9;
    long_why[600] = 0;
    rx_refuse(&e, long_why);
    assert(strcmp(e.fail, "refused") == 0 && strlen(e.why) == 254 && memcmp(e.why, long_why, 254) == 0);
    rx_wedit_free(&e);
    rx_buf_free(&b);
}

static void actions(void) {
    rx_actions l = {0};
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0}, want = {NULL, 0, 0};
    const char *edit = "1\n2 2 1\n#11 1 1 1 1\nx";
    rx_wedit_file(&e, NULL, 1, 2);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    rx_action(&l, "quickfix", "Add x", &e, NULL, 0);
    assert(e.body.data == NULL && e.group.data == NULL && e.files == 0);   /* used up */
    rx_action(&l, "source", "Organize imports", NULL, "organize", 8);
    rx_actions_finish(&l, &b);
    rx_buf_fmt(&want, "2\nquickfix 5 %zu 0\nAdd x%ssource 16 0 8\nOrganize importsorganize", strlen(edit), edit);
    same(&b, want.data);
    rx_buf_clear(&b);

    /* no kind is quickfix; a given empty edit is "0"; an edit that cannot be sent leaves its action out */
    rx_wedit_file(&e, NULL, 1, 2);
    rx_refuse(&e, "no");
    rx_action(&l, "refactor", "dropped", &e, NULL, 0);
    assert(!e.fail);
    rx_action(&l, " ", "Two\nlines", &e, "d\n", 2);
    rx_actions_finish(&l, &b);
    same(&b, "1\nquickfix 9 2 2\nTwo lines0\nd\n");
    for (int i = 0; i < 1001; i++) rx_action(&l, "source", "t", NULL, NULL, 0);
    assert(l.n == 1000 && l.full);
    rx_buf_clear(&b);
    rx_actions_finish(&l, &b);
    assert(memcmp(b.data, "1000\n", 5) == 0);
    rx_buf_free(&b);
    rx_buf_free(&want);
}

/* rhun drops a whole answer for one field past its cap (12d): names, titles, details and previews
 * are cut to 1000 bytes at a character, kind words to 40; a symbol or action whose name or title is
 * then empty, and a location whose path is over 4096 bytes, are left out; a workspace edit with such
 * a path cannot be sent */
static void cuts(void) {
    char straddle[1011], exact[1001], kind[42], path[4098], line[1004];
    memset(straddle, 'x', 999), straddle[999] = (char)0xc3, straddle[1000] = (char)0xa9;   /* é at 999 and 1000 */
    memset(straddle + 1001, 'y', 9), straddle[1010] = 0;
    memset(exact, 'x', 999), exact[999] = 'z', exact[1000] = 0;
    memset(kind, 'k', 39), kind[39] = (char)0xc3, kind[40] = (char)0xa9, kind[41] = 0;
    path[0] = '/', memset(path + 1, 'p', 4095), path[4096] = 'q', path[4097] = 0;    /* 4097 bytes */
    memcpy(line, "a\nb", 3), memset(line + 3, 'c', 1000), line[1003] = 0;
    rx_buf b = {NULL, 0, 0}, want = {NULL, 0, 0};

    rx_locations l = {0};
    rx_location(&l, path, 0, R(1, 1, 1, 1), "left out");
    path[4096] = 0;                                                                    /* 4096 bytes */
    rx_buf_fmt(&want, "  %s  ", straddle);
    rx_location(&l, path, 0, R(1, 1, 1, 2), want.data);
    rx_location(&l, NULL, 2, R(-1, 0, 7, 3), line);
    rx_locations_finish(&l, &b);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "2\n1 1 1 2 4096 999\n%s%.999s0 0 7 3 2 1000\n#2a b%.997s", path, straddle, line + 3);
    same(&b, want.data);

    rx_symbols sy = {0};
    rx_buf_clear(&b);
    rx_symbol(&sy, R(1, 1, 1, 1), kind, 3, straddle, exact);
    rx_symbol(&sy, R(2, 1, 2, 1), "\t", 0, "\n", NULL);         /* a space is a name; no kind left: function */
    rx_symbols_finish(&sy, &b);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "2\n1 1 1 1 %.39s 3 999 1000\n%.999s%s2 1 2 1 function 0 1 0\n ", kind, straddle, exact);
    same(&b, want.data);

    rx_actions ac = {0};
    rx_wedit e = {0};
    rx_buf_clear(&b);
    char k50[51];
    memset(k50, 'k', 50), k50[50] = 0;
    rx_action(&ac, k50, straddle, NULL, "d", 1);
    rx_wedit_file(&e, NULL, 1, 2);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    rx_action(&ac, "source", "", &e, NULL, 0);                  /* no title: left out, the edit used up */
    assert(e.body.data == NULL && e.group.data == NULL && e.files == 0);
    rx_action(&ac, "a\x01" "b\x7f c", exact, NULL, NULL, 0);
    rx_actions_finish(&ac, &b);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "2\n%.40s 999 0 1\n%.999sdabc 1000 0 0\n%s", k50, straddle, exact);
    same(&b, want.data);

    /* a workspace edit with a path over 4096 bytes cannot be sent; 4096 can */
    rx_buf_clear(&b);
    rx_wedit_file(&e, path, 0, RX_ON_DISK);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    assert(rx_wedit_finish(&e, &b) == 0 && b.len == strlen("1\n4096 - 1\n") + 4096 + strlen("1 1 1 1 1\nx"));
    rx_buf_clear(&b);
    path[4096] = 'q';
    rx_wedit_file(&e, NULL, 1, 2);
    rx_wedit_file(&e, path, 0, RX_ON_DISK);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    assert(rx_wedit_finish(&e, &b) == -1 && b.len == 0);
    assert(strcmp(e.fail, "toobig") == 0 && strcmp(e.why, "a path over 4096 bytes") == 0);
    rx_wedit_free(&e);
    /* an action whose edit names such a path is left out */
    rx_wedit_file(&e, path, 0, RX_ON_DISK);
    rx_action(&ac, "quickfix", "far", &e, NULL, 0);
    assert(!e.fail);
    rx_actions_finish(&ac, &b);
    same(&b, "0\n");
    rx_buf_free(&b);
    rx_buf_free(&want);
}

/* ---- rhun's requests answered by rx_run, through files in place of the pipes */

static char seen[64];       /* each action event's data, then | */

static int on_definition(rx_ext *ext, const rx_ask *req, rx_locations *out, void *user) {
    (void)ext, (void)user;
    assert(req->id == 1 && req->doc == 4 && req->version == 9 && strcmp(req->kind, "invoked") == 0 && !*req->ch);
    rx_range r;
    r.start = r.end = req->at;
    rx_location(out, NULL, req->doc, r, "x");
    return 1;
}

static int on_references(rx_ext *ext, const rx_ask *req, rx_locations *out, void *user) {
    (void)ext, (void)req, (void)out, (void)user;
    return 1;                                       /* none */
}

static int on_symbols(rx_ext *ext, const rx_ask *req, rx_symbols *out, void *user) {
    (void)ext, (void)req, (void)user;
    rx_symbol(out, R(0, 0, 0, 0), NULL, 0, "main", NULL);
    return 1;
}

static int on_actions(rx_ext *ext, const rx_actions_ask *req, rx_actions *out, void *user) {
    (void)ext, (void)user;
    assert(req->id == 4 && req->range.start.line == 1 && req->range.start.col == 2 && req->range.end.line == 3 &&
           req->range.end.col == 4);
    rx_action(out, "source", "Go", NULL, "a b\nc", 5);
    return 1;
}

static void on_action(rx_ext *ext, const unsigned char *data, size_t len, void *user) {
    (void)ext, (void)user;
    assert(data[len] == 0 && strlen(seen) + len + 1 < sizeof seen);
    strcat(seen, (const char *)data);
    strcat(seen, "|");
}

static int on_rename(rx_ext *ext, const rx_rename_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)user;
    if (strcmp(req->name, "bad") == 0) return rx_refuse(out, "not a name\nhere");
    if (strcmp(req->name, "big") == 0) {
        for (int i = 0; i < 1001; i++) rx_wedit_file(out, NULL, req->doc, req->version);
        return 1;
    }
    rx_range r;
    r.start = r.end = req->at;
    rx_wedit_file(out, NULL, req->doc, req->version);
    rx_wedit_edit(out, r, req->name, strlen(req->name));
    return 1;
}

static int on_format(rx_ext *ext, const rx_format_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)user;
    if (!req->spaces) return 1;                     /* nothing changes */
    rx_wedit_file(out, NULL, req->doc, req->version);
    rx_wedit_edit(out, R(1, 1, 1, 2), "        ", (size_t)req->tab_width);
    return 1;
}

static void on_ready(rx_ext *ext, const char *project, void *user) {
    (void)user;
    assert(!*project && rx_has(ext, "format") && rx_has(ext, "actions") && !rx_has(ext, "views"));
    /* an action event comes while the toast waits: it is delivered after this handler */
    assert(rx_toast(ext, "hi") == 0 && !*seen);
    rx_wedit e = {0};
    rx_wedit_file(&e, "/p/a.go", 0, RX_ON_DISK);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    assert(rx_apply(ext, &e) == 0 && e.body.data == NULL && e.files == 0);
    rx_wedit_file(&e, NULL, 4, 8);
    rx_wedit_edit(&e, R(1, 1, 1, 2), "", 0);
    assert(rx_apply(ext, &e) == -1 && strcmp(rx_error(ext), "stale document changed") == 0);
    /* past the limits: not sent, and no id used */
    for (int i = 0; i < 1001; i++) rx_wedit_file(&e, NULL, 4, 8);
    assert(rx_apply(ext, &e) == -1 && strcmp(rx_error(ext), "toobig more than 1000 files") == 0 && !e.fail);
    assert(rx_status(ext, "") == 0);
}

/* runs an extension on n bytes of input and returns what rx_run did; what it wrote goes to got */
static int run_bytes(const char *features, const rx_handlers *h, const char *input, size_t n, rx_buf *got) {
    FILE *in = fopen("rhunext-test.in", "wb");
    assert(in != NULL);
    size_t wrote = fwrite(input, 1, n, in);
    assert(wrote == n);
    fclose(in);
    FILE *r = freopen("rhunext-test.in", "rb", stdin), *w = freopen("rhunext-test.out", "wb", stdout);
    assert(r != NULL && w != NULL);
    int code = rx_run(features, h, NULL);
    fflush(stdout);
    FILE *out = fopen("rhunext-test.out", "rb");
    assert(out != NULL);
    char chunk[4096];
    size_t k;
    while ((k = fread(chunk, 1, sizeof chunk, out)) > 0) rx_buf_add(got, chunk, k);
    fclose(out);
    return code;
}

/* runs an extension on input until bye */
static void run(const char *features, const rx_handlers *h, const char *input, rx_buf *got) {
    int code = run_bytes(features, h, input, strlen(input), got);
    assert(code == 0);
}

static void ok(rx_buf *want, int id, const char *body) {
    rx_buf_fmt(want, "ok %d %zu\n%s", id, strlen(body), body);
}

static void dispatch(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_ready;
    h.definition = on_definition;
    h.references = on_references;
    h.symbols = on_symbols;
    h.actions = on_actions;
    h.action = on_action;
    h.rename = on_rename;
    h.format = on_format;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0};
    run("definition references symbols actions rename format", &h,
        "0 features definition references symbols actions rename format\n"
        "0 ready -\n"
        "0 action 3\nxyz"
        "ok 1\n"
        "ok 2\n"
        "error 3 stale document changed\n"
        "ok 4\n"
        "1 definition 4 9 2 3 invoked -\n"
        "2 references 4 9 2 3 invoked -\n"
        "3 symbols 4 9 2 3 invoked -\n"
        "4 actions 4 9 1 2 3 4\n"
        "0 action 5\na b\nc"
        "5 rename 4 9 2 3 invoked y\n"
        "6 rename 4 9 2 3 invoked bad\n"
        "7 format 4 9 4 no\n"
        "8 rename 4 9 2 3 invoked\n"
        "9 format 4 9 4 maybe\n"
        "10 complete 4 9 1 1 typed -\n"
        "11 format 4 9 4 yes\n"
        "12 rename 4 9 2 3 invoked big\n"
        "13 symbols 4 x 2 3 invoked -\n"
        "14 definition 4 9 2\n"
        "0 bye\n",
        &got);
    const char *apply1 = "1\n7 - 1\n/p/a.go1 1 1 1 1\nx", *apply2 = "1\n2 8 1\n#41 1 1 2 0\n";
    rx_buf_str(&want, "hello 1 definition references symbols actions rename format\n");
    rx_buf_str(&want, "1 toast 2\nhi");
    rx_buf_fmt(&want, "2 apply %zu\n%s", strlen(apply1), apply1);
    rx_buf_fmt(&want, "3 apply %zu\n%s", strlen(apply2), apply2);
    rx_buf_str(&want, "4 status 0\n");
    ok(&want, 1, "1\n2 3 2 3 2 1\n#4x");
    ok(&want, 2, "0\n");
    ok(&want, 3, "1\n0 0 0 0 function 0 4 0\nmain");
    ok(&want, 4, "1\nsource 2 0 5\nGoa b\nc");
    ok(&want, 5, "1\n2 9 1\n#42 3 2 3 1\ny");
    rx_buf_str(&want, "error 6 refused not a name here\n");
    ok(&want, 7, "0\n");
    rx_buf_str(&want, "error 8 unknown no such request\n");
    rx_buf_str(&want, "error 9 unknown no such request\n");
    rx_buf_str(&want, "error 10 unknown no such request\n");
    ok(&want, 11, "1\n2 9 1\n#41 1 1 2 4\n    ");
    rx_buf_str(&want, "error 12 toobig more than 1000 files\n");
    rx_buf_str(&want, "error 13 unknown no such request\n");
    rx_buf_str(&want, "error 14 unknown no such request\n");
    same(&got, want.data);
    assert(strcmp(seen, "xyz|a b\nc|") == 0);

    /* without handlers each is unknown, and an action event's body is still read past */
    memset(&h, 0, sizeof h);
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    run("", &h,
        "1 definition 4 9 2 3 invoked -\n"
        "2 references 4 9 2 3 invoked -\n"
        "3 symbols 4 9 2 3 invoked -\n"
        "4 actions 4 9 1 1 1 1\n"
        "0 action 6\n7 x\ny\n"
        "5 rename 4 9 2 3 invoked y\n"
        "6 format 4 9 4 yes\n"
        "0 bye\n",
        &got);
    rx_buf_str(&want, "hello 1\n");
    for (int i = 1; i <= 6; i++) rx_buf_fmt(&want, "error %d unknown no such request\n", i);
    same(&got, want.data);
    rx_buf_free(&got);
    rx_buf_free(&want);
}

/* ---- event lengths: checked before anything is allocated */

static size_t big_len;          /* the action event's body, its length; then what the toast said */
static int big_events, wait_result;
static char wait_error[512];

static void on_big_action(rx_ext *ext, const unsigned char *data, size_t len, void *user) {
    (void)ext, (void)data, (void)user;
    big_len = len;
}

static void on_any_event(rx_ext *ext, const rx_event *ev, void *user) {
    (void)ext, (void)ev, (void)user;
    big_events++;
}

static void on_ready_wait(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    wait_result = rx_toast(ext, "hi");
    snprintf(wait_error, sizeof wait_error, "%s", rx_error(ext));
    assert(rx_status(ext, "") == -1);          /* the session is over: no more requests */
}

static void on_ready_text(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    size_t n = 0;
    assert(rx_text(ext, 1, &n, NULL) == NULL);
    snprintf(wait_error, sizeof wait_error, "%s", rx_error(ext));
}

static void event_lengths(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.action = on_big_action;
    h.event = on_any_event;
    rx_buf got = {NULL, 0, 0}, in = {NULL, 0, 0};
    /* 4 MB is read whole */
    rx_buf_str(&in, "0 action 4194304\n");
    for (int i = 0; i < 4096; i++) {
        char k[1024];
        memset(k, 'x', sizeof k);
        rx_buf_add(&in, k, sizeof k);
    }
    rx_buf_str(&in, "0 bye\n");
    assert(run_bytes("", &h, in.data, in.len, &got) == 0 && big_len == ((size_t)4 << 20) && big_events == 2);
    /* past 4 MB, or not a plain decimal of at most 10 digits: the session ends there (1), and what
     * follows is never read as messages */
    const char *bad[] = {"0 action 4194305\n", "0 action 99999999999\n", "0 action +5\n", "0 action 5x\n",
                         "0 action \n", "0 action -1\n", "0 edits 1 2 4194305\n", "0 edits 1 2 1e3\n"};
    for (size_t i = 0; i < sizeof bad / sizeof bad[0]; i++) {
        rx_buf_clear(&in);
        rx_buf_clear(&got);
        big_len = 7, big_events = 0;
        rx_buf_fmt(&in, "%s0 run x.y 1\n0 bye\n", bad[i]);
        assert(run_bytes("", &h, in.data, in.len, &got) == 1);
        assert(big_len == 7 && big_events == 0);
    }
    /* while a request waits: it fails, rx_error says why, and rx_run returns once its handler does */
    memset(&h, 0, sizeof h);
    h.ready = on_ready_wait;
    h.event = on_any_event;
    rx_buf_clear(&got);
    big_events = 0;
    const char *input = "0 ready -\n0 action 4194305\nok 1\n0 run x.y 1\n0 bye\n";
    assert(run_bytes("", &h, input, strlen(input), &got) == 1);
    same(&got, "hello 1\n1 toast 2\nhi");
    assert(wait_result == -1 && strncmp(wait_error, "closed a body's length", 22) == 0 && big_events == 1);
    /* a reply's body is held to the same digits */
    h.ready = on_ready_text;
    rx_buf_clear(&got);
    wait_error[0] = 0;
    input = "0 ready -\nok 1 3 +5\nhello0 bye\n";
    assert(run_bytes("", &h, input, strlen(input), &got) == 1);
    same(&got, "hello 1\n1 text 1\n");
    assert(strncmp(wait_error, "closed a body's length", 22) == 0);
    rx_buf_free(&got);
    rx_buf_free(&in);
}

static void cursors(void) {
    rx_list l = {0};
    rx_range r = {{0, 0}, {0, 0}};
    rx_list_item_cursor(&l, r, "function", "f", "f()", NULL, NULL, NULL, NULL, 0, 2);
    rx_list_item_cursor(&l, r, "function", "g", "\xc3\xa9", NULL, NULL, NULL, NULL, 0, 1);   /* inside é */
    rx_list_item_cursor(&l, r, "function", "h", NULL, NULL, NULL, NULL, NULL, 0, -1);
    rx_buf out = {NULL, 0, 0};
    rx_list_finish(&l, &out);
    const char *want = "3 0\n0 0 0 0 function 1 3 0 11\nff()cursor 2 0\n0 0 0 0 function 1 2 0 0\ng\xc3\xa9"
                       "0 0 0 0 function 1 1 0 0\nhh";
    assert(out.len == strlen(want) && memcmp(out.data, want, out.len) == 0);
    rx_buf_free(&out);
}

static void command_removal_guards(void) {
    rx_ext ext = {0};
    assert(rx_unregister(&ext, "old") == -1);
    assert(strncmp(rx_error(&ext), "unknown ", 8) == 0);
    strcpy(ext.features, "commands");
    const char *bad[] = {NULL, "", "UPPER", "x\n2 unregister other", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"};
    for (size_t i = 0; i < sizeof bad / sizeof bad[0]; i++) {
        assert(rx_unregister(&ext, bad[i]) == -1);
        assert(strncmp(rx_error(&ext), "syntax ", 7) == 0);
    }
}

static int on_save_actions(rx_ext *ext, const rx_save_actions_ask *req, rx_wedit *out, void *user) {
    rx_wedit intermediate = {0};
    (void)out, (void)user;
    assert(req->id == 24 && req->doc == 7 && req->version == 9 && strcmp(req->kind, "source.fixAll") == 0);
    assert(rx_save_apply(ext, req->id, req->doc, req->version, &intermediate) == 10);
    return 1;
}

static void save_actions(void) {
    rx_ext ext = {0};
    rx_wedit edit = {0};
    assert(rx_save_apply(&ext, 24, 7, 9, &edit) == -1 && strncmp(rx_error(&ext), "unknown ", 8) == 0);
    strcpy(ext.features, "save-actions");
    assert(rx_save_apply(&ext, 2147483648LL, 7, 9, &edit) == -1 && strncmp(rx_error(&ext), "syntax ", 7) == 0);
    assert(rx_source_kind("source") && rx_source_kind("source.fixAll.eslint") && rx_source_kind("source.a_b-1"));
    assert(!rx_source_kind("quickfix") && !rx_source_kind("source.") && !rx_source_kind("source..x") && !rx_source_kind("source.\xc3\xa9"));
    int64_t number;
    assert(rx_positive_num("9223372036854775807", INT64_MAX, &number) && number == INT64_MAX);
    assert(!rx_positive_num("9223372036854775808", INT64_MAX, &number));
    rx_handlers h = {0};
    h.save_actions = on_save_actions;
    rx_buf got = {0};
    run("save-actions", &h,
        "0 features save-actions\n24 save-actions 7 9 source.fixAll\nok 1 10\n"
        "25 save-actions 7 9 source.\n26 save-actions 7 -1 source\n"
        "27 save-actions 9223372036854775808 9 source\n28 save-actions 7 9 source extra\n0 bye\n", &got);
    same(&got, "hello 1 save-actions\n1 save-apply 24 7 9 2\n0\nok 24 2\n0\n"
               "error 25 unknown no such request\nerror 26 unknown no such request\n"
               "error 27 unknown no such request\nerror 28 unknown no such request\n");
    rx_buf_clear(&got);
    run("save-actions", &h, "24 save-actions 7 9 source.fixAll\n0 bye\n", &got);
    same(&got, "hello 1 save-actions\nerror 24 unknown no such request\n");
    rx_buf_free(&got);
}

static void prepared_snippets(void) {
    rx_snippet_field fields[] = {{1,0,1,0},{1,2,3,0},{0,3,3,0}};
    rx_snippet snippet = {"x x",3,fields,3};
    rx_buf out = {0};
    assert(rx_snippet_encode(&snippet,&out)==0);
    same(&out,"0 0 0 0 snippet 0 3 0 48\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\n");
    rx_buf_free(&out);
    rx_snippet_field final_fields[]={{0,0,6,0}};
    rx_snippet final_snippet={"result",6,final_fields,1};
    assert(rx_snippet_encode(&final_snippet,&out)==0);
    same(&out,"0 0 0 0 snippet 0 6 0 16\nresultfield 0 0 6 0 0\n");rx_buf_free(&out);
    rx_snippet_field crossing[]={{1,0,1,0},{1,1,2,0},{0,1,3,0}};
    final_snippet.text="xxy";final_snippet.len=3;final_snippet.fields=crossing;final_snippet.nfields=3;
    assert(rx_snippet_validate(&final_snippet));
    rx_snippet_field nested[]={{1,0,1,0},{2,0,1,1},{2,2,3,0},{0,3,3,0}};
    snippet.fields=nested;snippet.nfields=4;assert(rx_snippet_validate(&snippet));
    rx_snippet_field cycle[]={{1,0,1,0},{2,2,3,0},{2,0,1,1},{1,2,3,2}};
    snippet.fields=cycle;assert(!rx_snippet_validate(&snippet));
    rx_buf_str(&out,"unchanged");assert(rx_snippet_encode(&snippet,&out)==-1);
    same(&out,"unchanged");rx_buf_free(&out);
    rx_snippet_field invalid[][4]={
        {{1,0,4,0}},{{1,1,0,0}},{{-1,0,1,0}},{{2147483648LL,0,1,0}},
        {{0,2,1,0}},{{0,0,0,0},{0,0,0,0}},{{1,0,1,1}},
        {{1,0,1,2},{2,0,1,0}},{{1,0,1,0},{2,1,2,1}},
        {{1,0,1,0},{1,1,2,0},{2,1,2,2}},
        {{1,0,2,0},{2,1,1,0}},{{1,0,2,0},{2,0,2,0}},
        {{1,0,2,0},{2,1,3,0}}
    };
    const size_t counts[]={1,1,1,1,1,2,1,2,2,3,2,2,2};
    for(size_t i=0;i<sizeof counts/sizeof counts[0];i++) {
        rx_snippet bad={"xxx",3,invalid[i],counts[i]};assert(!rx_snippet_validate(&bad));
    }
    rx_snippet_field different[]={{1,0,1,0},{1,1,2,0}};
    rx_snippet bad={"xy",2,different,2};assert(!rx_snippet_validate(&bad));
    const char *invalid_utf8[]={"\xc0\x80","\xed\xa0\x80","\xf4\x90\x80\x80","\xe0\x80\x80","\xf0\x80\x80\x80","\xc3"};
    for(size_t i=0;i<sizeof invalid_utf8/sizeof invalid_utf8[0];i++) {
        bad.text=invalid_utf8[i];bad.len=strlen(bad.text);bad.nfields=0;assert(!rx_snippet_validate(&bad));
    }
    rx_snippet_field unicode[]={{1,0,4,0},{2,0,4,1},{0,4,4,2}};
    bad.text="\xf0\x9f\x98\x80";bad.len=4;bad.fields=unicode;bad.nfields=3;
    assert(rx_snippet_validate(&bad));unicode[0].start=1;assert(!rx_snippet_validate(&bad));
    rx_snippet_field limits[513];memset(limits,0,sizeof limits);
    for(size_t i=0;i<513;i++)limits[i].group=1;
    bad.text="";bad.len=0;bad.fields=limits;bad.nfields=512;
    assert(rx_snippet_validate(&bad));bad.nfields=513;assert(!rx_snippet_validate(&bad));
    for(size_t i=0;i<257;i++)limits[i].group=(int64_t)i+1;
    bad.nfields=256;assert(rx_snippet_validate(&bad));bad.nfields=257;assert(!rx_snippet_validate(&bad));
    for(size_t i=0;i<33;i++)limits[i].parent=i;
    bad.nfields=32;assert(rx_snippet_validate(&bad));bad.nfields=33;assert(!rx_snippet_validate(&bad));
    char *large=(char *)malloc(4u<<20);assert(large);memset(large,'x',4u<<20);
    bad.text=large;bad.len=4u<<20;bad.nfields=0;assert(!rx_snippet_validate(&bad));free(large);
    for(int i=0;i<3;i++) {
        rx_list list={0};rx_snippet_field empty={(int64_t)(i==1),0,0,0};
        rx_snippet prep={"",0,&empty,i?1u:0u};
        rx_list_item_snippet(&list,R(0,0,0,0),"text","label",&prep,NULL,NULL,NULL,NULL,0);
        rx_list_finish(&list,&out);
        if(i==0)same(&out,"1 0\n0 0 0 0 text 5 0 0 0\nlabel");
        else assert(!strstr(out.data,"labellabel")&&strstr(out.data,"text 5 0 0 "));
        rx_buf_free(&out);
    }
    rx_list list={0};bad.text="x";bad.len=1;bad.fields=invalid[0];bad.nfields=1;
    rx_list_item_snippet(&list,R(0,0,0,0),"text","label",&bad,NULL,NULL,NULL,NULL,0);
    rx_list_finish(&list,&out);same(&out,"1 0\n0 0 0 0 text 5 1 0 0\nlabelx");rx_buf_free(&out);
    rx_ext ext={0};
    assert(rx_insert_snippet(&ext,1,0,R(1,1,1,1),&snippet)==-1&&!strncmp(rx_error(&ext),"unknown ",8));
    strcpy(ext.features,"snippets");
    assert(rx_insert_snippet(&ext,0,0,R(1,1,1,1),&snippet)==-1&&!strncmp(rx_error(&ext),"syntax ",7));
}

static void snippet_programs(void) {
    rx_snippet_field fields[]={{1,0,1,0},{1,2,3,0},{0,3,3,0}};
    rx_snippet s={"x x",3,fields,3};
    rx_snippet_option options[]={{"x",1},{NULL,0},{"a\n😀",6}};
    rx_snippet_choice choices[]={{1,options,3},{1,options,3}};
    rx_snippet_program p={&s,choices,1, NULL, 0, NULL, 0};
    rx_buf out={NULL,0,0};
    assert(rx_snippet_program_encode(&p,&out)==0);
    same(&out,"0 0 0 0 snippet 0 3 0 75\nx xfield 1 0 1 0 0\nfield 1 2 3 0 0\nfield 0 3 3 0 0\nchoice 1 3 13\n1\nx0\n6\na\n😀");
    rx_buf_free(&out);
    rx_list list={0};
    rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
    rx_list_finish(&list,&out);
    assert(strstr(out.data,"choice 1 3 13\n1\nx0\n6\na\n😀"));
    rx_buf_free(&out);
    for (int i=0;i<8;i++) {
        p.nchoices=1;choices[0].group=1;choices[0].noptions=3;choices[0].options=options;
        options[0].text="x";options[0].len=1;options[1].text=NULL;options[1].len=0;
        switch(i) {
            case 0:choices[0].group=0;break;
            case 1:choices[0].group=2;break;
            case 2:p.nchoices=2;break;
            case 3:choices[0].noptions=0;break;
            case 4:choices[0].noptions=257;break;
            case 5:options[0].text="y";break;
            case 6:options[1].text="\xff";options[1].len=1;break;
            default:options[1].len=(1u<<20)+1;break;
        }
        assert(!rx_snippet_program_validate(&p));
        rx_buf_str(&out,"unchanged");assert(rx_snippet_program_encode(&p,&out)==-1);same(&out,"unchanged");rx_buf_free(&out);
        rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
        rx_list_finish(&list,&out);same(&out,"1 0\n0 0 0 0 text 5 3 0 0\nlabelx x");rx_buf_free(&out);
    }
    rx_snippet_option many[256];memset(many,0,sizeof many);many[0].text="x";many[0].len=1;
    many[255].text="last\0😀\n";many[255].len=10;
    choices[0].group=1;choices[0].options=many;choices[0].noptions=256;p.nchoices=1;
    assert(rx_snippet_program_validate(&p));
    choices[0].options=NULL;assert(!rx_snippet_program_validate(&p));choices[0].options=many;
    p.choices=NULL;assert(!rx_snippet_program_validate(&p));p.choices=choices;
    p.nchoices=257;assert(!rx_snippet_program_validate(&p));p.nchoices=1;
    char *big=(char *)malloc(1u<<20);memset(big,'a',1u<<20);
    rx_snippet_option large[]={{NULL,0},{big,1u<<20},{big,1u<<20},{big,1u<<20},{big,(1u<<20)-200}};
    fields[0].end=0;s.text=NULL;s.len=0;s.nfields=1;choices[0].options=large;choices[0].noptions=5;
    assert(rx_snippet_program_encode(&p,&out)==0);
    large[4].len+=(4u<<20)-out.len;rx_buf_clear(&out);
    assert(rx_snippet_program_encode(&p,&out)==0&&out.len==(4u<<20));
    large[4].len++;assert(!rx_snippet_program_validate(&p));rx_buf_free(&out);free(big);
}

static void snippet_variants(void) {
    rx_snippet_field fields[]={{1,0,1,0},{1,2,5,0},{0,5,5,0}};
    rx_snippet s={"a ABC",5,fields,3};size_t indices[]={2,2};
    rx_snippet_program p={&s,NULL,0,indices,1, NULL, 0};rx_buf out={0};
    assert(!rx_snippet_validate(&s));assert(rx_snippet_program_encode(&p,&out)==0);
    same(&out,"0 0 0 0 snippet 0 5 0 60\na ABCfield 1 0 1 0 0\nfield 1 2 5 0 0\nfield 0 5 5 0 0\nvariant 2 0\n");rx_buf_free(&out);
    rx_list list={0};rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
    indices[0]=1;rx_list_finish(&list,&out);assert(strstr(out.data,"variant 2 0\n"));rx_buf_free(&out);
    size_t bad[]={0,1,3,4,SIZE_MAX};
    for(size_t i=0;i<sizeof bad/sizeof *bad;i++) {indices[0]=bad[i];assert(!rx_snippet_program_validate(&p));}
    indices[0]=2;p.ninitial_variants=2;assert(!rx_snippet_program_validate(&p));p.ninitial_variants=513;assert(!rx_snippet_program_validate(&p));
    p.ninitial_variants=1;p.initial_variants=NULL;assert(!rx_snippet_program_validate(&p));p.initial_variants=indices;
    s.text="a a";s.len=3;fields[1].end=3;s.nfields=2;assert(!rx_snippet_program_validate(&p));
    rx_ext ext={0};strcpy(ext.features,"snippets");assert(rx_insert_snippet_program(&ext,1,0,R(1,1,1,1),&p)==-1&&!strncmp(rx_error(&ext),"unknown ",8));
}

static void on_snippet_ready(rx_ext *ext,const char *project,void *user) {
    (void)project;(void)user;
    rx_snippet s={"",0,NULL,0};
    assert(rx_insert_snippet(ext,7,9,R(1,1,1,1),&s)==10);
    assert(rx_insert_snippet(ext,7,10,R(1,1,1,1),&s)==-1);
    rx_snippet_field field={1,0,0,0};s.fields=&field;s.nfields=1;
    rx_snippet_option options[]={{NULL,0},{"x",1}};
    rx_snippet_choice choice={2,options,2};rx_snippet_program p={&s,&choice,1, NULL, 0, NULL, 0};
    assert(rx_insert_snippet_program(ext,7,10,R(1,1,1,1),&p)==-1);
    choice.group=1;
    assert(rx_insert_snippet_program(ext,7,10,R(1,1,1,1),&p)==11);
}

static void snippet_requests(void) {
    rx_handlers h={0};h.ready=on_snippet_ready;
    rx_buf out={0};
    run("snippets",&h,"0 features snippets\n0 ready -\nok 1 10\nok 2 9\nok 3 11\n0 bye\n",&out);
    const char *body="0 0 0 0 snippet 0 0 0 0\n";
    rx_buf want={0};rx_buf_str(&want,"hello 1 snippets\n");
    rx_buf_fmt(&want,"1 snippet 7 9 1 1 1 1 %zu\n%s2 snippet 7 10 1 1 1 1 %zu\n%s",strlen(body),body,strlen(body),body);
    const char *program="0 0 0 0 snippet 0 0 0 34\nfield 1 0 0 0 0\nchoice 1 2 5\n0\n1\nx";
    rx_buf_fmt(&want,"3 snippet 7 10 1 1 1 1 %zu\n%s",strlen(program),program);
    same(&out,want.data);rx_buf_free(&out);rx_buf_free(&want);
}

static void snippet_snapshot_decoder(void) {
    const char *text="context 1 1 3 8 0 1 5 6 4 2 2 2\n😀\n  😀/p/a/p  //";
    rx_buf body={0},fields={0};rx_buf_str(&body,text);rx_buf_fmt(&fields,"9 %zu",body.len);
    rx_snippet_snapshot s;
    assert(rx_snapshot_decode(&fields,&body,9,R(1,3,1,3).start,&s));
    same(&s.selected_text,"😀\n");same(&s.current_line,"  😀");same(&s.indent,"  ");
    assert(s.tab_width==8 && !s.insert_spaces && s.crlf && s.version==9);
    rx_snippet_snapshot_free(&s);assert(!s.current_line.data && !s.version);
    for (size_t i=0;i<body.len;i++) {
        char old=body.data[i];body.data[i]=(char)255;
        assert(!rx_snapshot_decode(&fields,&body,9,R(1,3,1,3).start,&s));
        assert(!s.current_line.data && !s.version);body.data[i]=old;
    }
    const char *bad[]={"context 1 1 4 8 0 1 5 6 4 2 2 2\n😀\n  😀/p/a/p  //",
        "context 1 1 3 17 0 1 5 6 4 2 2 2\n😀\n  😀/p/a/p  //",
        "context 1 1 3 8 2 1 5 6 4 2 2 2\n😀\n  😀/p/a/p  //",
        "context 1 1 3 8 0 1 5 6 4 2 1 2\n😀\n  😀/p/a/p //",
        "context 1 1 3 8 0 1 0 4 0 0 0 0\n😀",
        "context 1 1 3 8 0 1 0 3 0 0 2 0\n  \n  ","context 1 1 3 8 0 1 +0 0 0 0 0 0\n"};
    for (size_t i=0;i<sizeof bad/sizeof *bad;i++) {
        rx_buf_clear(&body);rx_buf_str(&body,bad[i]);rx_buf_clear(&fields);rx_buf_fmt(&fields,"9 %zu",body.len);
        assert(!rx_snapshot_decode(&fields,&body,9,R(1,3,1,3).start,&s));
    }
    rx_buf_free(&body);rx_buf_free(&fields);
}

static int snapshot_cap_test;
static void on_snapshot_ready(rx_ext *ext,const char *project,void *user) {
    (void)project;(void)user;rx_snippet_snapshot s;
    assert(rx_get_snippet_snapshot(ext,0,9,R(1,3,1,3),&s)==-1);
    if (snapshot_cap_test) {
        assert(rx_get_snippet_snapshot(ext,7,9,R(1,3,1,3),&s)==-1 && !s.current_line.data);
        assert(ext->gone);return;
    }
    assert(rx_get_snippet_snapshot(ext,7,9,R(1,3,1,3),&s)==0);
    same(&s.current_line,"  😀");same(&s.indent,"  ");rx_snippet_snapshot_free(&s);
    assert(rx_toast(ext,"after")==0);
}

static void snippet_snapshot_requests(void) {
    rx_handlers h={0};h.ready=on_snapshot_ready;rx_buf input={0},out={0};
    const char *body="context 1 1 3 4 1 0 0 6 4 2 2 0\n  😀/p/a/p  ";
    rx_buf_fmt(&input,"0 features snippets\n0 ready -\n0 active 7\nok 1 9 %zu\n%sok 2\n0 bye\n",strlen(body),body);
    run("snippets",&h,input.data,&out);
    same(&out,"hello 1 snippets\n1 snippet-context 7 9 1 3 1 3\n2 toast 5\nafter");
    rx_buf_free(&out);rx_buf_free(&input);snapshot_cap_test=1;
    const char *bad[]={"4194305","+0","-1","wat","9223372036854775808"};
    for(size_t i=0;i<sizeof bad/sizeof *bad;i++) {
        rx_buf_fmt(&input,"0 features snippets\n0 ready -\nok 1 9 %s\n",bad[i]);
        assert(run_bytes("snippets",&h,input.data,input.len,&out)==1);
        same(&out,"hello 1 snippets\n1 snippet-context 7 9 1 3 1 3\n");
        rx_buf_free(&out);rx_buf_free(&input);
    }
}

struct st_fixture {
    const unsigned char *base;
    size_t len;
    int patch;
    unsigned char value;
    int valid;
    size_t offset;
};
#include "snippet_transform_fixtures.h"
static void snippet_transform_programs(void) {
    unsigned char empty[32] = {'S','N','T','X',1,0,0,0,0,0,16,0,0,0,0,0,32};
    size_t offset=99, maximum=0;
    assert(rx_snippet_transform_validate(empty,sizeof(empty),&offset)==1 && offset==0);
    assert(rx_snippet_transform_validate(empty,sizeof(empty),NULL)==1);
    empty[0]=0; empty[20]=1;
    assert(!rx_snippet_transform_validate(empty,sizeof(empty),&offset) && offset==0);
    empty[0]='S';
    assert(!rx_snippet_transform_validate(empty,sizeof(empty),&offset) && offset==20);
    assert(!rx_snippet_transform_validate(NULL,0,&offset) && offset==0);
    assert(!rx_snippet_transform_validate(NULL,31,&offset) && offset==31);
    assert(!rx_snippet_transform_validate(NULL,32,&offset) && offset==0);
    assert(!rx_snippet_transform_validate(NULL,SIZE_MAX,&offset) && offset==16);
    for (size_t i=0;i<sizeof(st_fixtures)/sizeof(st_fixtures[0]);i++)
        if (st_fixtures[i].len>maximum) maximum=st_fixtures[i].len;
    int part=99;
    assert(!rx_snippet_transform_program_validate(NULL,&part,&offset) && part==0 && offset==0);
    rx_snippet_transform_program pair={NULL,0,NULL,0};
    assert(!rx_snippet_transform_program_validate(&pair,&part,&offset) && part==1 && offset==0);
    pair.regex_len=262145;
    assert(!rx_snippet_transform_program_validate(&pair,&part,&offset) && part==1 && offset==40);
    pair.regex_len=80;pair.replacement_len=4u<<20;
    assert(!rx_snippet_transform_program_validate(&pair,&part,&offset) && part==0 && offset==0);
    for (size_t i=0;i<sizeof(st_fixtures)/sizeof(st_fixtures[0]);i++) {
        const struct st_fixture *v=&st_fixtures[i];
        if (!v->valid || v->patch>=0 || !rx_st_word(v->base,12)) continue;
        size_t roff=rx_st_word(v->base,36), foff=rx_st_word(v->base,44), flen=rx_st_word(v->base,48);
        pair.regex=v->base+roff;pair.regex_len=foff-roff;
        pair.replacement=v->base+foff;pair.replacement_len=flen;
        assert(rx_snippet_transform_program_validate(&pair,&part,&offset) && part==0 && offset==0);
        assert(rx_snippet_transform_program_validate(&pair,NULL,NULL));
        pair.replacement_len=0;
        assert(!rx_snippet_transform_program_validate(&pair,&part,&offset) && part==2 && offset==0);
        break;
    }
    unsigned char *allocation=(unsigned char *)malloc(maximum+2);
    assert(allocation);
    unsigned char *input=allocation+1; /* deliberately unaligned */
    for (size_t i=0;i<sizeof(st_fixtures)/sizeof(st_fixtures[0]);i++) {
        const struct st_fixture *v=&st_fixtures[i];
        memcpy(input,v->base,v->len);
        if (v->patch>=0) input[(size_t)v->patch]=v->value;
        allocation[0]=0xa5; input[v->len]=0x5a;
        offset=99;
        int result=rx_snippet_transform_validate(input,v->len,&offset);
        if (result!=v->valid || offset!=v->offset) {
            fprintf(stderr,"transform fixture %zu: got %d/%zu, want %d/%zu\n",i,result,offset,v->valid,v->offset);
            abort();
        }
        assert(allocation[0]==0xa5 && input[v->len]==0x5a);
        for (size_t j=0;j<v->len;j++) assert(input[j]==(v->patch>=0 && (size_t)v->patch==j ? v->value : v->base[j]));
    }
    free(allocation);
}
static void snippet_transform_wire(void) {
    static const unsigned char regex[]={82,78,82,88,1,0,0,0,0,0,16,0,0,0,0,0,0,0,0,0,2,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,80,0,0,0,0,0,0,0,1,0,0,0,97,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0};
    static const unsigned char format[]={82,78,82,70,1,0,0,0,0,0,16,0,1,0,0,0,1,0,0,0,57,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,120};
    rx_snippet_field fields[]={{1,0,1,0},{1,2,3,0},{0,3,3,0}};
    rx_snippet s={"a",1,fields,1};
    rx_snippet_field_transform bindings[]={{1,{regex,sizeof regex,format,sizeof format}},{2,{regex,sizeof regex,format,sizeof format}}};
    rx_snippet_program p={&s,NULL,0,NULL,0,bindings,1};rx_buf out={0};
    assert(rx_snippet_program_encode(&p,&out)==0);
    same(&out,"0 0 0 0 snippet 0 1 0 293\nafield 1 0 1 0 0\ntransforms 0 260\nU05UWAEAAAAAABAAAQAAAMEAAAAAAAAAAAAAAAAAAAABAAAAOAAAAFAAAACIAAAAOQAAAAAAAABSTlJYAQAAAAAAEAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAUAAAAAAAAAABAAAAYQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFJOUkYBAAAAAAAQAAEAAAABAAAAOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAeA==");rx_buf_free(&out);
    rx_list list={0};rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
    bindings[0].field=0;rx_list_finish(&list,&out);assert(strstr(out.data,"transforms 0 260\nU05UWAEAAAAAABAAAQAAAMEAAAAAAAAAAAAAAAAAAAABAAAAOAAAAFAAAACIAAAAOQAAAAAAAABSTlJYAQAAAAAAEAAAAAAAAAAAAAIAAAAAAAAAAAAAAAAAAAAAAAAAUAAAAAAAAAABAAAAYQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFJOUkYBAAAAAAAQAAEAAAABAAAAOQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAeA=="));rx_buf_free(&out);
    size_t bad[]={0,2,SIZE_MAX};
    for(size_t i=0;i<sizeof bad/sizeof *bad;i++) {
        bindings[0].field=bad[i];assert(!rx_snippet_program_validate(&p));
        rx_buf_str(&out,"unchanged");assert(rx_snippet_program_encode(&p,&out)<0);same(&out,"unchanged");rx_buf_free(&out);
        rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
        rx_list_finish(&list,&out);same(&out,"1 0\n0 0 0 0 text 5 1 0 0\nlabela");rx_buf_free(&out);
    }
    bindings[0].field=1;rx_ext ext={0};strcpy(ext.features,"snippets");
    assert(rx_insert_snippet_program(&ext,1,0,R(1,1,1,1),&p)<0&&!strncmp(rx_error(&ext),"unknown ",8));
    s.text="a a";s.len=3;s.nfields=3;p.ntransforms=2;
    assert(rx_snippet_program_encode(&p,&out)==0);rx_buf sorted=out;memset(&out,0,sizeof out);
    rx_snippet_field_transform swap=bindings[0];bindings[0]=bindings[1];bindings[1]=swap;
    assert(rx_snippet_program_encode(&p,&out)==0);same(&out,sorted.data);assert(bindings[0].field==2);rx_buf_free(&out);rx_buf_free(&sorted);
    bindings[0].field=1;assert(!rx_snippet_program_validate(&p));bindings[0].field=3;assert(!rx_snippet_program_validate(&p));
    p.ntransforms=513;assert(!rx_snippet_program_validate(&p));p.ntransforms=1;bindings[0]=bindings[1];
    s.nfields=1;
    size_t extra=rx_snippet_program_extra_size(&p);size_t length=(1u<<20)-128-5-extra;
    char *large=(char *)malloc(length+1);assert(large);memset(large,'z',length+1);s.text=large;s.len=length;
    assert(rx_snippet_program_validate(&p));rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
    assert(list.n==1&&!list.more);rx_list_finish(&list,&out);rx_buf_free(&out);
    s.len++;assert(rx_snippet_program_validate(&p));rx_list_item_snippet_program(&list,R(0,0,0,0),"text","label",&p,NULL,NULL,NULL,NULL,0);
    rx_list_finish(&list,&out);same(&out,"0 1\n");rx_buf_free(&out);free(large);
}
static void on_transform_ready(rx_ext *ext,const char *project,void *user) {
    (void)project;(void)user;
    static const unsigned char regex[]={82,78,82,88,1,0,0,0,0,0,16,0,0,0,0,0,0,0,0,0,2,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,80,0,0,0,0,0,0,0,1,0,0,0,97,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0}, format[]={82,78,82,70,1,0,0,0,0,0,16,0,1,0,0,0,1,0,0,0,57,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,1,0,0,0,0,0,0,0,0,0,0,0,120};
    rx_snippet_field field={1,0,1,0};rx_snippet snippet={"a",1,&field,1};
    rx_snippet_field_transform transform={1,{regex,sizeof regex,format,sizeof format}};
    rx_snippet_program p={&snippet,NULL,0,NULL,0,&transform,1};
    if(rx_has(ext,"snippets")&&rx_has(ext,"snippet-transforms")) assert(rx_insert_snippet_program(ext,7,9,R(1,1,1,1),&p)==10);
    else assert(rx_insert_snippet_program(ext,7,9,R(1,1,1,1),&p)==-1&&!strncmp(rx_error(ext),"unknown ",8));
    rx_toast(ext,"after");
}
static void transform_requests(void) {
    const char *agreed[]={"","snippets","snippet-transforms","snippets snippet-transforms"};
    rx_handlers h={0};h.ready=on_transform_ready;
    for(size_t i=0;i<4;i++) {
        rx_buf input={0},out={0};rx_buf_fmt(&input,"0 features %s\n0 ready -\n",agreed[i]);
        if(i==3) rx_buf_str(&input,"ok 1 10\n");
        rx_buf_str(&input,"0 bye\n");
        run("snippets snippet-transforms",&h,input.data,&out);
        if(i==3) {assert(strstr(out.data,"1 snippet 7 9 1 1 1 1 "));assert(strstr(out.data,"transforms 0 260\n"));assert(strstr(out.data,"2 toast 5\nafter"));}
        else same(&out,"hello 1 snippets snippet-transforms\n1 toast 5\nafter");
        rx_buf_free(&out);rx_buf_free(&input);
    }
}
/* retained views: a patch's bytes, as v1.1.md's view-tree section writes them */
static void notes_and_logs(void) {
    rx_note n;
    rx_note_init(&n, "Reload the window?");
    rx_note_button(&n, "Reload", "reload", NULL, 0);
    rx_note_button(&n, "Later", NULL, NULL, 0);
    rx_buf b = {NULL, 0, 0};
    rx_note_body(&n, &b);
    same(&b, "18 46\nReload the window?button 6\nReloadcommand reload 0\nbutton 5\nLater");
    rx_buf_free(&b);
    rx_note_free(&n);
    rx_patch p;
    memset(&p, 0, sizeof p);
    const char *sevs[] = {"info", NULL};
    const char *texts[] = {"start", "two\nlines"};
    rx_patch_log(&p, 2, sevs, texts);
    rx_patch_clear(&p);
    same(&p.body, "log 2 23\ninfo start\n- two lines\nclear 0\n");
    assert(p.n == 2);
    rx_patch_free(&p);
    rx_buf pic = {NULL, 0, 0};
    rx_pic_image(&pic, 0, 0, 40, 40.5, 7);
    same(&pic, "image 0 0 40 40.5 7\n");
    rx_buf_free(&pic);
    assert(rx_has_body("0 view 4 text 1"));
    assert(rx_has_body("0 view 4 paste 12"));
    assert(!rx_has_body("0 view 4 key ctrl a"));
}

static void retained_patches(void) {
    rx_patch p;
    memset(&p, 0, sizeof p);
    rx_patch_title(&p, "TODO");
    rx_patch_badge(&p, 3);
    rx_buf x = {NULL, 0, 0}, recs = {NULL, 0, 0};
    rx_x_badge(&x, 1);
    assert(rx_node_rec(&recs, "a", 1, "Alpha", "open", &x) == 0);
    assert(rx_node_rec(&recs, "a\nb", 3, "bad", "leaf", NULL) == -1);
    rx_patch_kids(&p, "", 0, &recs, 1, 0);
    same(&p.body, "title 4\nTODObadge 3 0\nkids 0 1 0 28\n1 5 10 open\naAlphabadge 1 0\n");
    assert(p.n == 3 && p.nodes == 1);
    rx_buf_free(&x);
    rx_buf_free(&recs);
    rx_patch_free(&p);
    rx_buf o = {NULL, 0, 0};
    rx_x_open(&o, "/p/a.rs", 2, 8);
    rx_x_icon(&o, "file:a.rs", NULL);
    same(&o, "open 2 8 7\n/p/a.rsicon file:a.rs - 0\n");
    rx_buf_free(&o);
    assert(rx_has_body("0 view 3 select 2 1 4"));
    assert(rx_has_body("0 view 3 activate 2 2"));
    assert(!rx_has_body("0 view 3 visible 1"));
    assert(!rx_has_body("0 view 3 open tree sidebar todo"));
    assert(rx_has_body("5 vkids 3 2 1"));
    assert(rx_has_body("6 vmore 3 2 4 1"));
    assert(!rx_has_body("7 complete 1 2 3 4 typed x"));
    rx_patch q;
    memset(&q, 0, sizeof q);
    rx_patch_action(&q, "pin", "pin", "node", "Pin");
    rx_patch_select_mode(&q, 1);
    rx_buf y = {NULL, 0, 0};
    rx_x_inline(&y, "pin");
    rx_x_menu(&y, "pin hide");
    same(&q.body, "action pin pin node 3\nPinselect-mode multi 0\n");
    same(&y, "inline 3\npinmenu 8\npin hide");
    rx_buf_free(&y);
    rx_patch_free(&q);
}

int main(void) {
    retained_patches();
    notes_and_logs();
    snippet_transform_wire();
    snippet_transform_programs();
    snippet_snapshot_decoder();
    prepared_snippets();
    snippet_programs();
    snippet_variants();
    command_removal_guards();
    cursors();
    old_tests();
    locations();
    symbols();
    wedits();
    actions();
    cuts();
    dispatch();                 /* last: standard output goes to a file from here on */
    save_actions();
    snippet_requests();
    transform_requests();
    snippet_snapshot_requests();
    event_lengths();
    fclose(stdout);
    remove("rhunext-test.in");
    remove("rhunext-test.out");
    fputs("c ok\n", stderr);
    return 0;
}
