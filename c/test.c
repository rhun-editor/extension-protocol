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

int main(void) {
    cursors();
    old_tests();
    locations();
    symbols();
    wedits();
    actions();
    cuts();
    dispatch();                 /* last: standard output goes to a file from here on */
    event_lengths();
    fclose(stdout);
    remove("rhunext-test.in");
    remove("rhunext-test.out");
    fputs("c ok\n", stderr);
    return 0;
}
