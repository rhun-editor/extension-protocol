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

static int resources_ok;
static void on_ready_resources(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    unsigned char *v = NULL;
    size_t n = 0;
    assert(rx_secret_set(ext, "token", "abc", 3) == 0);
    assert(rx_secret_get(ext, "token", &v, &n) == 1 && n == 3 && memcmp(v, "abc", 3) == 0);
    free(v);
    assert(rx_secret_get(ext, "none", &v, &n) == 0);
    char *keys = rx_secret_keys(ext, &n);
    assert(keys && n == 6 && strcmp(keys, "token\n") == 0);
    free(keys);
    assert(rx_secret_delete(ext, "token") == 0);
    assert(rx_busy(ext, 1) == 0);
    assert(rx_secret_get(ext, "Bad", &v, &n) == -1 && strncmp(rx_error(ext), "syntax ", 7) == 0);
    assert(rx_secret_set(ext, "big", "", 8193) == -1 && strncmp(rx_error(ext), "toobig ", 7) == 0);
    resources_ok = 1;
}

static int content_answer(rx_ext *ext, const char *uri, rx_content *out, void *user) {
    (void)ext, (void)user;
    if (strcmp(uri, "res-gen:/a b.txt") != 0) return 0;
    out->lang = "Visual Basic";
    out->title = "t";
    out->text = "hi\n";
    out->text_len = 3;
    return 1;
}
static char closed_uri[64];
static void on_content_closed(rx_ext *ext, const char *uri, void *user) {
    (void)ext, (void)user;
    snprintf(closed_uri, sizeof closed_uri, "%s", uri);
}

static int urls_ok;
static void on_ready_urls(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    assert(rx_open_url(ext, "ftp://x") == -1 && strncmp(rx_error(ext), "syntax ", 7) == 0);
    assert(rx_open_url(ext, "https://a b") == -1);
    assert(rx_open_url(ext, "https://x") == 1);
    assert(rx_open_url(ext, "https://y") == 2);
    assert(rx_open_url(ext, "https://z") == -1 && strncmp(rx_error(ext), "cancelled", 9) == 0);
    char *e = rx_external_uri(ext, "http://127.0.0.1:5/cb");
    assert(e && strcmp(e, "http://127.0.0.1:5/cb") == 0);
    free(e);
    char *l = rx_link(ext);
    assert(l && strcmp(l, "rhun://ext/o/n/abcdef0") == 0);
    free(l);
    urls_ok = 1;
}
static int64_t link_id;
static char link_got[64];
static void on_link(rx_ext *ext, int64_t id, const char *link, size_t len, void *user) {
    (void)ext, (void)user;
    link_id = id;
    snprintf(link_got, sizeof link_got, "%.*s", (int)len, link);
}

static char files_got[512];
static int found_paths_cb(const rx_found_path *p, int n, void *user) {
    (void)user;
    for (int i = 0; i < n; i++)
        snprintf(files_got + strlen(files_got), sizeof files_got - strlen(files_got), "%lld:%.*s ",
                 (long long)p[i].folder, (int)p[i].path_len, p[i].path);
    return 1;
}
static int found_files_cb(const rx_found_file *f, int n, void *user) {
    (void)user;
    for (int i = 0; i < n; i++)
        snprintf(files_got + strlen(files_got), sizeof files_got - strlen(files_got), "%.*s doc%lld %d:%d-%d pre%d [%.*s] ",
                 (int)f[i].path_len, f[i].path, (long long)f[i].doc, f[i].matches[0].line, f[i].matches[0].col,
                 f[i].matches[0].end, f[i].matches[0].pre, (int)f[i].matches[0].text_len, f[i].matches[0].text);
    return 0;   /* stop: the run is cancelled */
}
static int setting_ok;
static void on_ready_setting(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    char *v = rx_setting_get(ext, "style", 1, "Visual Basic");
    assert(v && strcmp(v, "goimports") == 0);
    free(v);
    v = rx_setting_get(ext, "none", 0, NULL);
    assert(!v && strncmp(rx_error(ext), "refused", 7) == 0);
    setting_ok = 1;
}
static int folders_seen;
static void on_folders_event(rx_ext *ext, const rx_event *ev, void *user) {
    (void)user;
    rx_folder f[4];
    if (rx_parse_folders(ev, f, 4) == 2) {
        assert(f[0].flags == 3 && f[1].id == 4 && f[1].path_len == 6 && memcmp(f[1].path, "/w/lib", 6) == 0);
        assert(rx_folder_of(ext, "/w/lib/x.go") == 4);
        folders_seen = 1;
    }
}
static int files_ok;
static void on_ready_files(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    const char *inc[] = {"*.c"}, *exc[] = {"vendor"};
    rx_file_query q = {0, 10, 0, inc, 1, exc, 1};
    rx_finished fin;
    assert(rx_find_files(ext, &q, found_paths_cb, NULL, &fin) == 0);
    assert(strcmp(files_got, "1:main.c 1:src/ 2:a.c ") == 0 && fin.count == 3 && strcmp(fin.status, "complete") == 0);
    files_got[0] = 0;
    rx_file_query all = {0, 5, RX_SEARCH_IGNORE_CASE, NULL, 0, NULL, 0};
    assert(rx_search_text(ext, "TODO", 4, 0, &all, found_files_cb, NULL, &fin) == -1);
    assert(strncmp(rx_error(ext), "cancelled", 9) == 0);
    assert(strcmp(files_got, "main.c doc3 2:4-8 pre1 [//TODO!] ") == 0);
    files_ok = 1;
}

/* the resources group: idle, secrets, uris, urls, files */
static void resources_requests(void) {
    rx_ext none = {0};
    assert(rx_busy(&none, 1) == -1 && strncmp(rx_error(&none), "unknown ", 8) == 0);
    assert(rx_secret_keys(&none, NULL) == NULL && strncmp(rx_error(&none), "unknown ", 8) == 0);
    assert(rx_secret_key_valid("github.token-2_x") && !rx_secret_key_valid("A") && !rx_secret_key_valid(""));
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_ready_resources;
    rx_buf got = {NULL, 0, 0};
    const char *input = "0 features idle secrets\n0 ready -\nok 1\nok 2 1 3\nabcok 3 0 0\nok 4 6\ntoken\n"
                        "ok 5\nok 6\n0 bye\n";
    run_bytes("idle secrets", &h, input, strlen(input), &got);
    same(&got, "hello 1 idle secrets\n1 secret-set token 3\nabc2 secret-get token\n3 secret-get none\n"
               "4 secret-keys\n5 secret-delete token\n6 busy yes\n");
    assert(resources_ok);
    rx_buf_clear(&got);
    assert(rx_uri_valid("lsp-expand:/a/main.rs") && !rx_uri_valid("C:x") && !rx_uri_valid("Ab:x") && !rx_uri_valid("ab:"));
    memset(&h, 0, sizeof h);
    h.content = content_answer;
    h.content_closed = on_content_closed;
    input = "0 features uris\n0 ready -\n7 content res-gen:/a b.txt\n8 content res-gen:/none\n"
            "0 content-closed res-gen:/a b.txt\n0 bye\n";
    run_bytes("uris", &h, input, strlen(input), &got);
    same(&got, "hello 1 uris\nok 7 23\n12 1 3\nVisual_Basicthi\nerror 8 nofile no such document\n");
    assert(strcmp(closed_uri, "res-gen:/a b.txt") == 0);
    /* urls: open-url, external-uri, link and the link event */
    rx_buf_clear(&got);
    assert(rx_open_url(&none, "https://x") == -1 && strncmp(rx_error(&none), "unknown ", 8) == 0);
    memset(&h, 0, sizeof h);
    h.ready = on_ready_urls;
    h.link = on_link;
    input = "0 features urls\n0 ready -\nok 1 opened\nok 2 copied\nerror 3 cancelled no\n"
            "ok 4 21\nhttp://127.0.0.1:5/cbok 5 22\nrhun://ext/o/n/abcdef0\n0 link 5 26\nrhun://ext/o/n/abcdef0/x?y0 bye\n";
    run_bytes("urls", &h, input, strlen(input), &got);
    same(&got, "hello 1 urls\n1 open-url 9\nhttps://x2 open-url 9\nhttps://y3 open-url 9\nhttps://z"
               "4 external-uri 21\nhttp://127.0.0.1:5/cb5 link\n");
    assert(urls_ok && link_id == 5 && strcmp(link_got, "rhun://ext/o/n/abcdef0/x?y") == 0);
    /* files: batches while the request runs, a cancel when the callback says stop */
    rx_buf_clear(&got);
    memset(&h, 0, sizeof h);
    h.ready = on_ready_files;
    input = "0 features files\n0 ready -\n0 toast-ish 1\n0 found 1 20\n2\n1 6\nmain.c1 4\nsrc/0 found 1 9\n1\n2 3\na.c"
            "ok 1 3 complete\n0 found 2 35\n1\n1 3 7 6 1\nmain.c2 4 8 1 7\n//TODO!ok 3\nerror 2 cancelled\n0 bye\n";
    run_bytes("files", &h, input, strlen(input), &got);
    same(&got, "hello 1 files\n1 find-files 0 10 0 13\n+*.c\n-vendor\n2 search-text 0 5 16 8\n4 0\nTODO3 cancel 2\n");
    assert(files_ok);
    /* scoped-settings: setting-get */
    rx_buf_clear(&got);
    memset(&h, 0, sizeof h);
    h.ready = on_ready_setting;
    input = "0 features scoped-settings\n0 ready -\nok 1 9\ngoimportserror 2 refused not a setting of this extension\n0 bye\n";
    run_bytes("scoped-settings", &h, input, strlen(input), &got);
    same(&got, "hello 1 scoped-settings\n1 setting-get style 1 Visual_Basic\n2 setting-get none 0 -\n");
    assert(setting_ok);
    /* workspace-folders: the folders event and folder-of */
    rx_buf_clear(&got);
    memset(&h, 0, sizeof h);
    h.event = on_folders_event;
    input = "0 features workspace-folders\n0 folders 48\n2\n1 local 3 3 6\napp/w/app4 local 2 3 6\nlib/w/libok 1 4\n0 bye\n";
    run_bytes("workspace-folders", &h, input, strlen(input), &got);
    same(&got, "hello 1 workspace-folders\n1 folder-of /w/lib/x.go\n");
    assert(folders_seen);
    rx_buf_free(&got);
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

/* protocol 1.1's agents, proposals and tools: the bodies */
static void proposals_are_12g(void) {
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0};
    rx_wedit_file_hashed(&e, "/p/a.txt", "x\r\n", 3);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "y", 1);
    rx_wedit_label(&e, "Fix");
    rx_wedit_confirm(&e);
    assert(rx_proposal_body("L", "s1", &e, &b) == 0);
    same(&b, "1 2\nLs1" "1\n8 - 1\n/p/a.txt1 1 1 1 1\ny");
    rx_buf_free(&b), rx_wedit_free(&e);
    rx_wedit o = {0};
    rx_wedit_create(&o, "/p/b.txt", 0);
    assert(rx_proposal_body("L", NULL, &o, &b) < 0 && strcmp(o.fail, "unknown") == 0);
    rx_buf_free(&b), rx_wedit_free(&o);
}

static void ai_bodies(void) {
    proposals_are_12g();
    rx_transcript t;
    memset(&t, 0, sizeof t);
    t.total = 2;
    rx_transcript_add(&t, "tool", "read_file", "src/main.rs", 11, NULL);
    rx_buf b = {NULL, 0, 0};
    rx_transcript_finish(&t, &b);
    same(&b, "2 1\ntool 9 11 0\nread_filesrc/main.rs");
    rx_buf_free(&b);
    char a0[] = "3", a1[] = "1", a2[] = "partial", a3[] = "1", a4[] = "2";
    char *argv[] = {a0, a1, a2, a3, a4};
    rx_event ev;
    memset(&ev, 0, sizeof ev);
    ev.name = "proposal", ev.argc = 5, ev.argv = argv;
    rx_proposal_event pe;
    assert(rx_parse_proposal(&ev, &pe) && pe.proposal == 3 && pe.index == 1 && pe.accepted == 1 && pe.hunks == 2);
    char d1[] = "done", d2[] = "1", d3[] = "2";
    char *argv2[] = {a0, d1, d2, d3};
    ev.argv = argv2, ev.argc = 4;
    assert(rx_parse_proposal(&ev, &pe) && strcmp(pe.state, "done") == 0 && pe.accepted == 1 && pe.rejected == 2);
    const char *list = "1\nrhun.read ro 4 1 9\nReaddpath PATH";
    rx_tool *tools;
    int n;
    assert(rx_tools_parse((const unsigned char *)list, strlen(list), &tools, &n) == 0 && n == 1);
    assert(strcmp(tools[0].name, "rhun.read") == 0 && tools[0].readonly && strcmp(tools[0].input, "path PATH") == 0);
    rx_tools_free(tools, n);
    assert(rx_tools_parse((const unsigned char *)"2\nx ro 1 0 0\n", 13, &tools, &n) < 0);
}

/* ---- protocol 1.1: workspace edit transactions, ordered changes, the editor model */

static void transactions(void) {
    assert(rx_fnv1a64("", 0) == 0xcbf29ce484222325ull && rx_fnv1a64("a", 1) == 0xaf63dc4c8601ec8cull &&
           rx_fnv1a64("foobar", 6) == 0x85944171f73967e8ull);
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0}, want = {NULL, 0, 0};
    rx_wedit_label(&e, "Rename to 'total'\nnow");
    rx_wedit_delete(&e, "/p/old.c", RX_RECURSIVE | RX_IGNORE_MISSING);
    rx_wedit_file_hashed(&e, "/p/calc.c", "a\r\nb\r\n", 6);
    rx_wedit_edit(&e, R(2, 1, 2, 2), "B", 1);
    rx_wedit_rename(&e, "/p/util.c", "/p/helpers.c", RX_OVERWRITE);
    rx_wedit_file(&e, NULL, 3, 9);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "x", 1);
    rx_wedit_create(&e, "/p/new/", 0);
    rx_wedit_create(&e, "/p/new/a.c", RX_IGNORE_EXISTING);
    rx_wedit_confirm(&e);
    assert(rx_wedit_finish(&e, &b) == 0);
    rx_buf_fmt(&want, "6\ncreate 7 -\n/p/new/create 10 ignore-existing\n/p/new/a.c"
                      "9 fnv1a64:%016llx 1\n/p/calc.c2 1 2 2 1\nB2 9 1\n#31 1 1 1 1\nx"
                      "rename 9 12 overwrite\n/p/util.c/p/helpers.c"
                      "delete 8 recursive,ignore-missing\n/p/old.c"
                      "label 21\nRename to 'total' nowconfirm 0\n",
               (unsigned long long)rx_fnv1a64("a\r\nb\r\n", 6));
    same(&b, want.data);
    /* without file-operations an edit with operations fails as unknown (tx_parts has the rest) */
    rx_buf_clear(&b);
    rx_wedit_create(&e, "/p/x", 0);
    assert(rx_wedit_finish_caps(&e, &b, 1, 0, NULL) == -1 && strcmp(e.fail, "unknown") == 0 && b.len == 0);
    rx_wedit_free(&e);
    rx_wedit_delete(&e, "/p/x", RX_OVERWRITE);
    assert(rx_wedit_finish(&e, &b) == -1 && strcmp(e.fail, "syntax") == 0);
    rx_wedit_free(&e);
    for (int i = 0; i < 1001; i++) rx_wedit_delete(&e, "/p/x", 0);
    assert(rx_wedit_finish(&e, &b) == -1 && strcmp(e.why, "more than 1000 entries") == 0);
    rx_wedit_free(&e);
    rx_buf_free(&b);
    rx_buf_free(&want);
}

/* an edit of kind k: 0 a hashed group, 1 labelled, 2 with confirm, 3 a labelled rename, 4 plain text */
static void tx_edit(rx_wedit *e, int k) {
    if (k == 3) {
        rx_wedit_label(e, "Move");
        rx_wedit_rename(e, "/p/a.c", "/p/b.c", 0);
        return;
    }
    if (k == 0) rx_wedit_file_hashed(e, "/p/a.c", "x", 1);
    else rx_wedit_file(e, NULL, 4, 7);
    rx_wedit_edit(e, R(1, 1, 1, 1), "y", 1);
    if (k == 1) rx_wedit_label(e, "L");
    if (k == 2) rx_wedit_confirm(e);
}

/* what only the transaction grammar has needs workspace-edits, and never goes in a 12g body */
static void tx_parts(void) {
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0};
    char want[200];
    const char *parts[3] = {"fnv1a64 precondition", "label", "confirm record"};
    for (int k = 0; k < 3; k++) {
        tx_edit(&e, k);
        assert(rx_wedit_finish_caps(&e, &b, 0, 0, NULL) == -1 && strcmp(e.fail, "unknown") == 0 && b.len == 0);
        snprintf(want, sizeof want, "the edit's %s needs the workspace-edits feature, which was not agreed", parts[k]);
        assert(strcmp(e.why, want) == 0);
        rx_wedit_free(&e);
        tx_edit(&e, k);
        assert(rx_wedit_finish_caps(&e, &b, 1, 1, "a format answer") == -1 && b.len == 0);
        snprintf(want, sizeof want, "a format answer keeps 12g's grammar, which has no %s", parts[k]);
        assert(strcmp(e.why, want) == 0);
        rx_wedit_free(&e);
        tx_edit(&e, k);
        assert(rx_wedit_finish_caps(&e, &b, 1, 0, NULL) == 0 && b.len > 0);
        rx_buf_clear(&b);
    }
    int caps[3][2] = {{0, 0}, {1, 0}, {0, 1}};
    for (int i = 0; i < 3; i++) {
        tx_edit(&e, 3);
        assert(rx_wedit_finish_caps(&e, &b, caps[i][0], caps[i][1], NULL) == -1 && b.len == 0 &&
               strcmp(e.why, "file operations need the workspace-edits and file-operations features") == 0);
        rx_wedit_free(&e);
    }
    tx_edit(&e, 3);
    assert(rx_wedit_finish_caps(&e, &b, 0, 0, "propose") == -1 &&
           strcmp(e.why, "propose keeps 12g's grammar, which has no file operations") == 0);
    rx_wedit_free(&e);
    tx_edit(&e, 3);
    assert(rx_wedit_finish(&e, &b) == 0);   /* the whole grammar */
    same(&b, "1\nrename 6 6 -\n/p/a.c/p/b.clabel 4\nMove");
    /* plain text edits are 12g's body whatever was agreed */
    const char *fixed[3] = {NULL, NULL, "a format answer"};
    for (int i = 0; i < 3; i++) {
        rx_buf_clear(&b);
        tx_edit(&e, 4);
        rx_wedit_file(&e, "/p/d.c", 0, RX_ON_DISK);
        rx_wedit_edit(&e, R(1, 1, 1, 1), "y", 1);
        assert(rx_wedit_finish_caps(&e, &b, i == 1, i == 1, fixed[i]) == 0);
        same(&b, "2\n2 7 1\n#41 1 1 1 1\ny6 - 1\n/p/d.c1 1 1 1 1\ny");
    }
    /* a resolve answer's edit built outside a handler is the whole grammar; with a handler's caps
     * (1: neither feature) it cannot be sent, as an action of rx_action with those caps */
    rx_resolved r;
    memset(&r, 0, sizeof r);
    tx_edit(&r.edit, 3);
    rx_buf_clear(&b);
    assert(rx_resolved_finish(&r, &b) == 0 && b.len > 0);
    rx_wedit_free(&r.edit);
    r.caps = 1;
    tx_edit(&r.edit, 0);
    assert(rx_resolved_finish(&r, &b) == -1 && strcmp(r.edit.fail, "unknown") == 0);
    rx_wedit_free(&r.edit);
    rx_actions l;
    memset(&l, 0, sizeof l);
    l.caps = 1;
    tx_edit(&e, 1);
    rx_action(&l, NULL, "Labelled", &e, NULL, 0);
    assert(l.n == 0 && !e.fail);
    rx_buf_free(&b);
}

/* a change of kind k */
static rx_change chg(int kind, const char *path, const char *to, int n, const rx_range *r, const char *const *t) {
    rx_change c;
    memset(&c, 0, sizeof c);
    c.kind = kind, c.path = path, c.to = to, c.version = RX_ON_DISK, c.hashed = 1, c.hash = 7;
    c.ranges = r, c.texts = t, c.nedits = n;
    return c;
}

static void ordered(void) {
    rx_range x1 = R(1, 1, 1, 1), y2 = R(1, 2, 1, 2), at11 = R(1, 1, 1, 1);
    const char *tx[] = {"x"}, *ty[] = {"y"}, *tc[] = {"c"}, *tm[] = {"fn main() {}\n"}, *tg[] = {"g"};
    rx_change cs[10];
    cs[0] = chg(RX_CHANGE_EDIT, "/p/a.rs", NULL, 1, &x1, tx);
    cs[1] = chg(RX_CHANGE_RENAME, "/p/a.rs", "/p/b.rs", 0, NULL, NULL);
    cs[2] = chg(RX_CHANGE_EDIT, "/p/b.rs", NULL, 1, &y2, ty);
    cs[3] = chg(RX_CHANGE_EDIT, "/p/c.rs", NULL, 1, &at11, tc);
    cs[4] = chg(RX_CHANGE_CREATE, "/p/new.rs", NULL, 0, NULL, NULL);
    cs[5] = chg(RX_CHANGE_EDIT, "/p/new.rs", NULL, 1, &at11, tm);
    cs[6] = chg(RX_CHANGE_RENAME, "/p/dir", "/p/dir2", 0, NULL, NULL);
    cs[7] = chg(RX_CHANGE_EDIT, "/p/dir2/m.rs", NULL, 1, &at11, tx);
    cs[8] = chg(RX_CHANGE_EDIT, "/p/gone.rs", NULL, 1, &at11, tg);
    cs[9] = chg(RX_CHANGE_DELETE, "/p/gone.rs", NULL, 0, NULL, NULL);
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0};
    int origins[10], bad = 99;
    char why[200];
    assert(rx_wedit_from_ordered(&e, cs, 10, origins, &bad, why, sizeof why) == 0);
    assert(rx_wedit_finish(&e, &b) == 0);
    same(&b, "8\ncreate 9 -\n/p/new.rs7 fnv1a64:0000000000000007 2\n/p/a.rs1 1 1 1 1\nx1 1 1 1 1\ny"
             "7 fnv1a64:0000000000000007 1\n/p/c.rs1 1 1 1 1\nc9 - 1\n/p/new.rs1 1 1 1 13\nfn main() {}\n"
             "11 fnv1a64:0000000000000007 1\n/p/dir/m.rs1 1 1 1 1\nx"
             "rename 7 7 -\n/p/a.rs/p/b.rsrename 6 7 -\n/p/dir/p/dir2delete 10 -\n/p/gone.rs");
    int wantorig[8] = {4, 0, 3, 5, 7, 1, 6, 9};
    assert(memcmp(origins, wantorig, sizeof wantorig) == 0);
    /* what the phases cannot express */
    rx_range xy = R(1, 1, 1, 1);
    const char *txy[] = {"XY"}, *tbang[] = {"!"};
    rx_change bad1[2] = {chg(RX_CHANGE_DELETE, "/p/a", NULL, 0, NULL, NULL), chg(RX_CHANGE_EDIT, "/p/a", NULL, 1, &x1, tx)};
    rx_change bad2[2] = {chg(RX_CHANGE_RENAME, "/p/a", "/p/b", 0, NULL, NULL), chg(RX_CHANGE_RENAME, "/p/b", "/p/c", 0, NULL, NULL)};
    rx_change bad3[2] = {chg(RX_CHANGE_EDIT, "/p/b", NULL, 1, &x1, tx), chg(RX_CHANGE_RENAME, "/p/a", "/p/b", 0, NULL, NULL)};
    rx_change bad4[2] = {chg(RX_CHANGE_RENAME, "/p/d", "/p/e", 0, NULL, NULL), chg(RX_CHANGE_DELETE, "/p/d/f", NULL, 0, NULL, NULL)};
    rx_change bad5[2] = {chg(RX_CHANGE_EDIT, "/p/a", NULL, 1, &xy, txy), chg(RX_CHANGE_EDIT, "/p/a", NULL, 1, &y2, tbang)};
    const rx_change *bads[5] = {bad1, bad2, bad3, bad4, bad5};
    const char *parts[5] = {"after it was renamed away or deleted", "two file operations", "after an edit", "folder", "earlier edit"};
    for (int i = 0; i < 5; i++) {
        rx_wedit_free(&e);
        bad = 99;
        assert(rx_wedit_from_ordered(&e, bads[i], 2, NULL, &bad, why, sizeof why) == -1);
        if (bad != 1 || !strstr(why, parts[i])) {
            fprintf(stderr, "case %d: %d %s\n", i, bad, why);
            abort();
        }
    }
    /* where an edit's text comes from */
    char src[64];
    int created;
    assert(rx_locate(cs, 2, "/p/b.rs", src, sizeof src, &created) == 0 && strcmp(src, "/p/a.rs") == 0 && !created);
    assert(rx_locate(cs, 5, "/p/new.rs", src, sizeof src, &created) == 0 && created);
    assert(rx_locate(cs, 2, "/p/a.rs", src, sizeof src, &created) == -1);
    rx_wedit_free(&e);
    rx_buf_free(&b);
}

static char model_seen[512];

static void model_note(const char *s) {
    assert(strlen(model_seen) + strlen(s) + 2 < sizeof model_seen);
    strcat(model_seen, s);
    strcat(model_seen, "|");
}

static void on_model_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    rx_selection sels[2] = {{{1, 1}, {1, 4}}, {{2, 3}, {2, 3}}};
    assert(rx_select_many(ext, 4, sels, 2) == 0);
    assert(rx_select_many(ext, 4, sels, 0) == -1 && strncmp(rx_error(ext), "toobig", 6) == 0);
    rx_pos at = {2, 3};
    assert(rx_reveal(ext, 4, at, "center-if-outside") == 0);
    assert(rx_reveal(ext, 4, at, "middle") == -1 && strncmp(rx_error(ext), "syntax", 6) == 0);
    rx_locations l;
    memset(&l, 0, sizeof l);
    rx_location(&l, NULL, 4, R(2, 3, 2, 3), NULL);
    assert(rx_show_locations(ext, "Callers", &l) == -1 && strcmp(rx_error(ext), "busy the palette is open") == 0);
    int64_t tab = rx_diff(ext, 4, 5, "HEAD\nwork", 1);
    assert(tab == 77 && rx_diff_close(ext, tab) == 0);
    rx_wedit e = {0};
    rx_wedit_label(&e, "Move");
    rx_wedit_rename(&e, "/p/a.c", "/p/b.c", 0);
    rx_wedit_create(&e, "/p/c.c", 0);
    rx_wedit_file_hashed(&e, "/p/a.c", "x", 1);
    rx_wedit_edit(&e, R(2, 3, 2, 3), "y", 1);
    assert(rx_apply(ext, &e) == -1);
    const rx_apply_error *a = rx_last_apply_error(ext);
    assert(strcmp(a->code, "stale") == 0 && a->entry == 2 && strcmp(a->text, "/p/a.c changed on disk") == 0);
    model_note("ready");
}

static void on_model_event(rx_ext *ext, const rx_event *ev, void *user) {
    (void)ext, (void)user;
    rx_selection s[4];
    rx_doc d;
    char note[128];
    int n;
    if ((n = rx_parse_selections(ev, &d, s, 4)) > 0) {
        snprintf(note, sizeof note, "sel %lld %d %d:%d-%d:%d %d:%d", (long long)d, n, s[0].anchor.line, s[0].anchor.col,
                 s[0].cursor.line, s[0].cursor.col, s[1].cursor.line, s[1].cursor.col);
        model_note(note);
    }
}

static int on_model_linked(rx_ext *ext, const rx_linked_ask *req, rx_linked *out, void *user) {
    (void)ext, (void)user;
    assert(req->id == 9 && req->doc == 4 && req->version == 7 && req->at.line == 3 && req->at.col == 2);
    rx_linked_range(out, R(3, 2, 3, 5));
    rx_linked_range(out, R(4, 3, 4, 6));
    rx_linked_word(out, "-_\n-\xc3\xa9:");
    return 1;
}

static void on_uris_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    assert(rx_diff_uris(ext, "res-x:/a.txt", "file:///p/a.c", "t", 0) == 78);
    assert(rx_diff_uris(ext, "res-x:/a b", "5", "t", 0) == -1 && strncmp(rx_error(ext), "syntax", 6) == 0);
    rx_pos at = {2, 3};
    assert(rx_reveal_uri(ext, "res-x:/a.txt", at, "top") == 0);
    model_note("uris");
}

static void uri_sides(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_uris_ready;
    rx_buf got = {NULL, 0, 0};
    model_seen[0] = 0;
    run("reveal diff", &h, "0 features reveal diff\n0 ready -\nok 1 78\nok 2\n0 bye\n", &got);
    same(&got, "hello 1 reveal diff\n1 diff res-x:/a.txt file:///p/a.c show 1\nt2 reveal res-x:/a.txt 2 3 top\n");
    assert(strcmp(model_seen, "uris|") == 0);
    rx_buf_free(&got);
}

static void editor_model(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_model_ready;
    h.event = on_model_event;
    h.linked = on_model_linked;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0};
    const char *feats = "workspace-edits file-operations selections reveal diff linked-editing";
    run(feats, &h,
        "0 features workspace-edits file-operations selections reveal diff linked-editing\n"
        "0 ready -\n"
        "ok 1\n"
        "ok 2\n"
        "error 3 busy the palette is open\n"
        "ok 4 77\n"
        "ok 5\n"
        "error 6 stale 2 /p/a.c changed on disk\n"
        "0 selections 4 2 16\n3 1 3 5\n1 2 1 2\n"
        "9 linked 4 7 3 2\n"
        "0 bye\n",
        &got);
    char edit[256];
    snprintf(edit, sizeof edit, "3\ncreate 6 -\n/p/c.c6 fnv1a64:%016llx 1\n/p/a.c2 3 2 3 1\nyrename 6 6 -\n/p/a.c/p/b.clabel 4\nMove",
             (unsigned long long)rx_fnv1a64("x", 1));
    rx_buf_fmt(&want, "hello 1 %s\n", feats);
    rx_buf_str(&want, "1 select-many 4 2 16\n1 1 1 4\n2 3 2 3\n");
    rx_buf_str(&want, "2 reveal 4 2 3 center-if-outside\n");
    rx_buf_str(&want, "3 show-locations 24\nCallers\n1\n2 3 2 3 2 0\n#4");
    rx_buf_str(&want, "4 diff 4 5 background 9\nHEAD work");
    rx_buf_str(&want, "5 diff-close 77\n");
    rx_buf_fmt(&want, "6 apply %zu\n%s", strlen(edit), edit);
    ok(&want, 9, "2\n3 2 3 5\n4 3 4 6\nword 3\n-_:");
    same(&got, want.data);
    assert(strcmp(model_seen, "ready|sel 4 2 3:1-3:5 1:2|") == 0);

    /* without the features: refused before sending, linked unknown, the body still read */
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    memset(&h, 0, sizeof h);
    h.linked = on_model_linked;
    run("", &h, "0 selections 4 1 8\n1 1 1 1\n9 linked 4 7 3 2\n0 bye\n", &got);
    same(&got, "hello 1\nerror 9 unknown no such request\n");
    rx_buf_free(&got);
    rx_buf_free(&want);
}


/* notebooks: nb-open and nb-save with their bodies, nb-run, the events, the senders */
static int64_t nb_ran = -1;
static char nb_what[16];
static const char *on_nb_open(rx_ext *ext, const rx_nb_open *req, rx_buf *out, void *user) {
    (void)ext, (void)user;
    if (req->len == 3 && memcmp(req->data, "bad", 3) == 0) return "not a toy notebook";
    assert(strcmp(req->path, "/a.nb") == 0 && req->nb == 4);
    rx_nb_begin(out, 1, "{}", 2);
    rx_nb_cell(out, "code", "python", -1, req->data, req->len, "", 0, 1);
    rx_nb_output(out, "stdout", NULL, "", 0, 1);
    rx_nb_item(out, "ansi", 0, 0, "hi\n", 3);
    return NULL;
}
static const char *on_nb_save(rx_ext *ext, const rx_nb_save *req, rx_buf *out, void *user) {
    (void)ext, (void)user;
    rx_nb_reader r;
    rx_nb_cell_rec c;
    rx_nb_output_rec o;
    rx_nb_item_rec it;
    assert(rx_nb_read_begin(&r, req->body, req->len) && r.cells == 1 && r.meta_len == 2);
    assert(rx_nb_read_cell(&r, &c) && strcmp(c.kind, "code") == 0 && strcmp(c.lang, "python") == 0 && c.outputs == 1);
    assert(rx_nb_read_output(&r, &o) && strcmp(o.kind, "stdout") == 0 && o.items == 1);
    assert(rx_nb_read_item(&r, &it) && strcmp(it.type, "ansi") == 0 && it.len == 3);
    rx_buf_add(out, c.source, c.source_len);
    return NULL;
}
static void on_nb_run(rx_ext *ext, const rx_nb_run *run, void *user) {
    (void)user;
    nb_ran = run->run;
    assert(run->len == 3 && memcmp(run->source, "x=2", 3) == 0 && strcmp(run->lang, "python") == 0);
    assert(rx_nb_state(ext, run->nb, run->cell, run->run, "done", 1) == 0);
}
static void on_nb_event(rx_ext *ext, const char *what, int64_t nb, void *user) {
    (void)ext, (void)user, (void)nb;
    snprintf(nb_what, sizeof nb_what, "%s", what);
}
static void notebooks(void) {
    {   /* nb-exec's command body */
        rx_buf b = {NULL, 0, 0};
        const char *args[] = {"-m", "ipykernel_launcher"}, *env[] = {"A=b"};
        rx_command_body(&b, "python3", "", args, 2, env, 1);
        const char want[] = "7 0 2 1\npython32\n-m18\nipykernel_launcher3\nA=b";
        assert(b.len == sizeof want - 1 && memcmp(b.data, want, b.len) == 0);
        rx_buf_free(&b);
    }
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.nb_open = on_nb_open;
    h.nb_save = on_nb_save;
    h.nb_run = on_nb_run;
    h.nb_event = on_nb_event;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0}, body = {NULL, 0, 0}, in = {NULL, 0, 0};
    rx_nb_begin(&body, 1, "{}", 2);
    rx_nb_cell(&body, "code", "python", -1, "x=1", 3, "", 0, 1);
    rx_nb_output(&body, "stdout", NULL, "", 0, 1);
    rx_nb_item(&body, "ansi", 0, 0, "hi\n", 3);
    rx_buf_str(&in, "0 features notebooks\n0 ready -\n");
    rx_buf_str(&in, "1 nb-open 4 12\n5 3\n/a.nbx=1");
    rx_buf_str(&in, "2 nb-open 4 12\n5 3\n/a.nbbad");
    rx_buf_fmt(&in, "3 nb-save 4 %zu\n5\n/a.nb", body.len + 7);
    rx_buf_add(&in, body.data, body.len);
    rx_buf_str(&in, "0 nb-run 4 9 2 python 3\nx=2ok 1\n0 nb-interrupt 4\n0 bye\n");
    assert(run_bytes("notebooks", &h, in.data, in.len, &got) == 0);
    rx_buf_str(&want, "hello 1 notebooks\n");
    rx_buf_fmt(&want, "ok 1 %zu\n", body.len);
    rx_buf_add(&want, body.data, body.len);
    rx_buf_str(&want, "error 2 refused not a toy notebook\nok 3 3\nx=11 nb-state 4 9 2 done 1\n");
    same(&got, want.data);
    assert(nb_ran == 2 && strcmp(nb_what, "interrupt") == 0);
    rx_buf_free(&got);
    rx_buf_free(&want);
    rx_buf_free(&body);
    rx_buf_free(&in);
}

/* ---- protocol 1.1 language features (go/rhunext/query_test.go, the same bytes) */

static void item_records(void) {
    rx_list l = {0};
    rx_range repl = R(2, 5, 2, 9), elsewhere = R(1, 2, 1, 3);
    rx_item_extra x = {0};
    x.resolve = "r1", x.resolve_len = 2, x.resolve_edits = 1, x.replace = &repl;
    x.commit = "..(\x01", x.filter = "prt", x.suffix = "(s)";
    x.preselect = x.deprecated = x.adjust = x.after_signature = 1;
    rx_list_item_extra(&l, R(2, 5, 2, 7), "text", "print", NULL, NULL, NULL, NULL, NULL, 0, -1, &x);
    /* what is not valid is left out */
    static char big[(16 << 10) + 1];
    rx_item_extra y = {0};
    y.resolve = big, y.resolve_len = sizeof big, y.replace = &elsewhere, y.commit = " \t\xc3\xa9", y.after_complete = 1;
    rx_list_item_extra(&l, R(1, 1, 1, 2), "text", "x", NULL, NULL, NULL, NULL, NULL, 0, -1, &y);
    rx_buf out = {NULL, 0, 0};
    rx_list_finish(&l, &out);
    same(&out, "2 0\n2 5 2 7 text 5 5 0 119\nprintprintresolve 1 2\nr1replace 2 5 2 9 0\ncommit 2\n.(filter 3\nprt"
               "suffix 3\n(s)preselect 0\ndeprecated 0\nadjust 0\nafter signature 0\n"
               "1 1 1 2 text 1 1 0 17\nxxafter complete 0\n");
    rx_buf_clear(&out);
    rx_range edit = R(1, 1, 1, 1);
    const char *texts[] = {"use x;\n"};
    rx_resolve_answer(&out, "fn\nprint()", "Docs r1", &edit, texts, 1, 1, 1);
    same(&out, "detail 10\nfn print()doc 7\nDocs r1edit 1 1 1 1 7\nuse x;\nafter signature 0\nafter complete 0\n");
    rx_buf_clear(&out);
    rx_signature_info sigs[3] = {{"f(a)", 2, 3, NULL}, {"f(a, b)", 5, 6, "two"}, {"f()", 9, 12, NULL}};
    rx_signatures_answer(&out, sigs, 3, 1);
    same(&out, "3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()");
    rx_buf_clear(&out);
    rx_signatures_answer(&out, sigs, 0, 0);    /* none hides the help */
    same(&out, "");
    rx_signature_info many[40];
    for (int i = 0; i < 40; i++) many[i].label = "g()", many[i].from = many[i].to = -1, many[i].doc = NULL;
    rx_signatures_answer(&out, many, 40, 35);
    assert(out.len > 5 && memcmp(out.data, "32 0\n-1 -1 3 0\ng()", 18) == 0);
    rx_buf_clear(&out);
    /* past 64 KB the documentation goes, then overloads; a signature that cannot fit is none */
    static char doc[30001], label[70001];
    memset(doc, 'd', sizeof doc - 1);
    memset(label, 'x', sizeof label - 1);
    rx_signature_info docs[4] = {{"h()", -1, -1, doc}, {"h()", -1, -1, doc}, {"h()", -1, -1, doc}, {"h()", -1, -1, doc}};
    rx_signatures_answer(&out, docs, 4, 2);
    same(&out, "2 0\n-1 -1 3 0\nh()-1 -1 3 0\nh()");
    rx_buf_clear(&out);
    rx_signature_info huge = {label, -1, -1, NULL};
    rx_signatures_answer(&out, &huge, 1, 0);
    same(&out, "");
    rx_buf_free(&out);
}

static rx_buf query_seen;       /* what the handlers saw, each then | */

static void query_note(rx_ext *ext, rx_stream *s) {
    rx_buf_fmt(&query_seen, "%s %d|", rx_error(ext), rx_stream_cancelled(s));
}

static int query_saw(const char *want) {
    int same = query_seen.len == strlen(want) && memcmp(query_seen.data, want, query_seen.len) == 0;
    rx_buf_clear(&query_seen);
    return same;
}

static int on_locations_stream(rx_ext *ext, const rx_ask *req, rx_stream *s, rx_locations *out, void *user) {
    (void)user;
    if (rx_stream_progress(s, 40, "Indexing\nall") < 0) {
        query_note(ext, s);
        return 1;
    }
    rx_locations part = {{NULL, 0, 0}, 0, 0};
    rx_location(&part, NULL, req->doc, R(1, 1, 1, 2), "a");
    if (rx_stream_locations(s, &part) < 0) {
        query_note(ext, s);
        return 1;
    }
    rx_location(out, NULL, req->doc, R(2, 1, 2, 2), "b");
    return 1;
}

/* a cancel event while the part waits: the next part is not sent */
static int on_cancelled_stream(rx_ext *ext, const rx_ask *req, rx_stream *s, rx_locations *out, void *user) {
    (void)req, (void)out, (void)user;
    rx_locations part = {{NULL, 0, 0}, 0, 0};
    assert(!rx_stream_cancelled(s) && rx_stream_locations(s, &part) == 0 && rx_stream_cancelled(s));
    assert(rx_stream_part(s, "0\n", 2) == -1);
    query_note(ext, s);
    return 1;
}

static int on_complete_items(rx_ext *ext, const rx_ask *req, rx_stream *s, rx_buf *out, void *user) {
    (void)ext, (void)req, (void)s, (void)user;
    rx_list l = {0};
    rx_range repl = R(2, 5, 2, 9);
    rx_item_extra x = {0};
    x.resolve = "r1", x.resolve_len = 2, x.resolve_edits = 1, x.replace = &repl;
    x.commit = "..(\x01", x.filter = "prt", x.suffix = "(s)";
    x.preselect = x.deprecated = x.adjust = x.after_signature = 1;
    rx_list_item_extra(&l, R(2, 5, 2, 7), "text", "print", NULL, NULL, NULL, NULL, NULL, 0, -1, &x);
    rx_list_finish(&l, out);
    return 1;
}

static int on_resolve(rx_ext *ext, const rx_resolve_ask *req, rx_buf *out, void *user) {
    (void)ext, (void)user;
    assert(req->id == 4 && req->doc == 4 && req->version == 2 && req->len == 2 && req->data[2] == 0);
    char doc[64];
    snprintf(doc, sizeof doc, "Docs %s", (const char *)req->data);
    rx_range edit = R(1, 1, 1, 1);
    const char *texts[] = {"use x;\n"};
    rx_resolve_answer(out, "fn print()", doc, &edit, texts, 1, 0, 0);
    return 1;
}

static int on_signatures(rx_ext *ext, const rx_ask *req, rx_buf *out, void *user) {
    (void)ext, (void)req, (void)user;
    rx_signature_info sigs[3] = {{"f(a)", 2, 3, NULL}, {"f(a, b)", 5, 6, "two"}, {"f()", 9, 12, NULL}};
    rx_signatures_answer(out, sigs, 3, 1);
    return 1;
}

static void on_scope_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    int r = rx_provides(ext, "definition", "*.go");
    rx_buf_fmt(&query_seen, "%d %s|", r, r ? rx_error(ext) : "");
    if (!rx_has(ext, "provider-scope")) return;
    assert(rx_provides(ext, "references", "-") == -1 && strcmp(rx_error(ext), "refused no") == 0);
    assert(rx_provides(ext, "bad key", "*") == -1 && strncmp(rx_error(ext), "syntax ", 7) == 0);
    assert(rx_provides(ext, "definition", "a\nb") == -1 && strncmp(rx_error(ext), "syntax ", 7) == 0);
}

static void query_requests(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.references_stream = on_locations_stream;
    h.definition_stream = on_locations_stream;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0};
    run("references query-stream", &h,
        "0 features references query-stream\n0 ready -\n"
        "7 references 4 2 1 1 invoked -\nok 1\nok 2\n"
        "8 definition 4 2 1 1 invoked -\nok 3\nerror 4 cancelled\n"
        "0 bye\n",
        &got);
    const char *part = "1\n1 1 1 2 2 1\n#4a";
    rx_buf_str(&want, "hello 1 references query-stream\n1 query-progress 7 40 12\nIndexing all");
    rx_buf_fmt(&want, "2 query-part 7 %zu\n%s", strlen(part), part);
    ok(&want, 7, "1\n2 1 2 2 2 1\n#4b");
    rx_buf_fmt(&want, "3 query-progress 8 40 12\nIndexing all4 query-part 8 %zu\n%s", strlen(part), part);
    ok(&want, 8, "0\n");                            /* a part answered cancelled stops the handler */
    same(&got, want.data);
    assert(query_saw("cancelled 1|"));

    /* without the feature: refused before anything is sent */
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    run("references", &h, "0 features references\n7 references 4 2 1 1 invoked -\n0 bye\n", &got);
    rx_buf_str(&want, "hello 1 references\n");
    ok(&want, 7, "0\n");
    same(&got, want.data);
    assert(query_saw("unknown query-stream was not agreed 0|"));

    /* rhun withdraws the request while a part waits, or before the handler runs */
    h.references_stream = on_cancelled_stream;
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    run("references query-stream", &h,
        "0 features references query-stream\n9 references 4 2 1 1 invoked -\n0 cancel 9\nok 1\n0 bye\n", &got);
    rx_buf_str(&want, "hello 1 references query-stream\n1 query-part 9 2\n0\n");
    ok(&want, 9, "0\n");
    same(&got, want.data);
    assert(query_saw("cancelled rhun no longer needs the answer 1|"));

    /* complete-items records, a resolve request's body, signature lists */
    memset(&h, 0, sizeof h);
    h.complete_stream = on_complete_items;
    h.complete_resolve = on_resolve;
    h.signatures = on_signatures;
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    run("complete complete-items signature-list", &h,
        "0 features complete complete-items signature-list\n0 ready -\n"
        "3 complete 4 2 2 7 invoked -\n4 complete-resolve 4 2 2\nr15 signatures 4 2 2 7 invoked -\n0 bye\n",
        &got);
    const char *extra = "resolve 1 2\nr1replace 2 5 2 9 0\ncommit 2\n.(filter 3\nprtsuffix 3\n(s)preselect 0\n"
                        "deprecated 0\nadjust 0\nafter signature 0\n";
    rx_buf item = {NULL, 0, 0};
    rx_buf_fmt(&item, "1 0\n2 5 2 7 text 5 5 0 %zu\nprintprint%s", strlen(extra), extra);
    rx_buf_str(&want, "hello 1 complete complete-items signature-list\n");
    ok(&want, 3, item.data);
    ok(&want, 4, "detail 10\nfn print()doc 7\nDocs r1edit 1 1 1 1 7\nuse x;\n");
    ok(&want, 5, "3 1\n2 3 4 0\nf(a)5 6 7 3\nf(a, b)two-1 -1 3 0\nf()");
    same(&got, want.data);
    rx_buf_free(&item);

    /* without the features both are unknown, and the resolve request's body is read past */
    rx_buf_clear(&got);
    rx_buf_clear(&want);
    run("complete", &h, "0 features complete\n4 complete-resolve 4 2 2\nr15 signatures 4 2 2 7 invoked -\n0 bye\n", &got);
    same(&got, "hello 1 complete\nerror 4 unknown no such request\nerror 5 unknown no such request\n");
    /* a body length rhun never sends ends the session before anything is read */
    const char *bad[] = {"65537", "+1", "99999999999", "x"};
    for (size_t i = 0; i < sizeof bad / sizeof bad[0]; i++) {
        rx_buf_clear(&got);
        rx_buf_clear(&want);
        rx_buf_fmt(&want, "0 features complete-items\n4 complete-resolve 4 2 %s\n0 bye\n", bad[i]);
        assert(run_bytes("complete-items", &h, want.data, want.len, &got) == 1);
        same(&got, "hello 1 complete-items\n");
    }

    /* provides needs provider-scope */
    memset(&h, 0, sizeof h);
    h.ready = on_scope_ready;
    rx_buf_clear(&got);
    run("provider-scope", &h, "0 features provider-scope\n0 ready -\nok 1\nerror 2 refused no\n0 bye\n", &got);
    same(&got, "hello 1 provider-scope\n1 provides definition *.go\n2 provides references -\n");
    rx_buf_clear(&got);
    run("", &h, "0 ready -\n0 bye\n", &got);
    same(&got, "hello 1\n");
    assert(query_saw("0 |-1 unknown provider-scope was not agreed|"));
    rx_buf_free(&query_seen);
    rx_buf_free(&got);
    rx_buf_free(&want);
}
/* ---- navigation, prepare-rename, partial-format, code-actions (go/rhunext/actions_test.go, the
 * same bytes) */

static const char *const go_actions =
    "3\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1refactor.extract 7 0 21\nExtractcommand extract 3\n"
    "a bquickfix 6 0 23\nCannotdisabled 11\nNo fix here";

/* the actions of actions_test.go's probe */
static void probe_actions(rx_code_actions *l) {
    rx_action_extra fix = {0}, ext = {0}, cannot = {0}, nothing = {0};
    fix.preferred = 1, fix.resolve = "R", fix.resolve_len = 1, fix.fixes = "d1";
    ext.command = "extract", ext.args = "a b", ext.args_len = 3;
    cannot.disabled = "No fix here";
    rx_code_action(l, "quickfix", "Fix", NULL, &fix);
    rx_code_action(l, "refactor.extract", "Extract", NULL, &ext);
    rx_code_action(l, "quickfix", "Cannot", NULL, &cannot);
    rx_code_action(l, "quickfix", "Nothing", NULL, &nothing);
}

static void code_action_records(void) {
    rx_code_actions l = {0};
    rx_buf out = {NULL, 0, 0}, want = {NULL, 0, 0};
    probe_actions(&l);
    rx_code_action(&l, "quickfix", "Nothing", NULL, NULL);
    rx_code_actions_finish(&l, &out);
    same(&out, go_actions);
    assert(l.n == 0 && l.recs.data == NULL);
    /* the kinds asked leave out the others */
    rx_buf_clear(&out);
    l.kinds = "quickfix";
    probe_actions(&l);
    rx_code_actions_finish(&l, &out);
    same(&out, "2\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1quickfix 6 0 23\nCannotdisabled 11\nNo fix here");
    assert(rx_kind_wanted(NULL, "x") && rx_kind_wanted("", "x") && rx_kind_wanted("-", "x"));
    assert(rx_kind_wanted("refactor,source.organizeImports", "source.organizeImports.go"));
    assert(rx_kind_wanted("refactor,source.organizeImports", "refactor"));
    assert(!rx_kind_wanted("refactor,source.organizeImports", "source.organizeImportsX"));
    assert(!rx_kind_wanted("source.organizeImports", "source") && !rx_kind_wanted("quickfix", "refactor"));

    /* an edit is used up; one that cannot be sent leaves its action out; no kind is quickfix */
    rx_code_actions m = {0};
    rx_wedit e = {0};
    rx_wedit_file(&e, NULL, 4, 7);
    rx_wedit_edit(&e, R(1, 1, 1, 1), "R", 1);
    rx_code_action(&m, NULL, "Edit", &e, NULL);
    assert(e.body.data == NULL && e.files == 0);
    rx_wedit_file(&e, NULL, 4, 7);
    rx_refuse(&e, "no");
    rx_action_extra x = {0};
    x.disabled = "but disabled";
    rx_code_action(&m, "quickfix", "Dropped", &e, &x);
    assert(!e.fail);
    /* a command name or args rhun would refuse, resolve data past 16 KiB: the record is left out,
     * and with it an action that does nothing else */
    static char big[(16 << 10) + 1], args[8193];
    const char *names[] = {"", "Fix", "fix-it", "x23456789012345678901234567890123456789012"};
    for (size_t i = 0; i < sizeof names / sizeof names[0]; i++) {
        rx_action_extra c = {0};
        c.command = names[i];
        rx_code_action(&m, "quickfix", "Bad name", NULL, &c);
        assert(rx_command_ref(&out, names[i], NULL, 0) == -1);
    }
    rx_action_extra c = {0};
    c.command = "fix", c.args = args, c.args_len = sizeof args;
    rx_code_action(&m, "quickfix", "Long args", NULL, &c);
    rx_action_extra r = {0};
    r.resolve = big, r.resolve_len = sizeof big;
    rx_code_action(&m, "quickfix", "Big", NULL, &r);
    rx_code_action(&m, "quickfix", "", NULL, &x);         /* no title */
    /* a kind is cut at 128 bytes, a reason at 1000, at a character */
    static char kind[210], reason[1002];
    memcpy(kind, "refactor.", 9);
    memset(kind + 9, 'k', 200);
    memset(reason, 'r', 999);
    reason[999] = (char)0xc3, reason[1000] = (char)0xa9;
    x.disabled = reason;
    rx_code_action(&m, kind, "Long", NULL, &x);
    rx_buf_clear(&out);
    rx_code_actions_finish(&m, &out);
    const char *edit = "1\n2 7 1\n#41 1 1 1 1\nR";
    rx_buf_fmt(&want, "2\nquickfix 4 %zu 0\nEdit%s%.128s 4 0 1012\nLongdisabled 999\n%.999s", strlen(edit), edit, kind, reason);
    same(&out, want.data);
    for (int i = 0; i < 1001; i++) rx_code_action(&m, "source", "t", NULL, &x);
    assert(m.n == 1000 && m.full);
    rx_buf_clear(&out);
    rx_code_actions_finish(&m, &out);
    assert(memcmp(out.data, "1000\n", 5) == 0);

    /* command references */
    rx_buf_clear(&out);
    assert(rx_command_ref(&out, "fix_2", "a\nb", 3) == 0 && rx_command_ref(&out, "fix", args, sizeof args) == -1);
    assert(rx_command_ref(&out, "fix", args, sizeof args - 1) == 0 && out.len == 19 + 17 + 8192);
    assert(memcmp(out.data, "command fix_2 3\na\nbcommand fix 8192\n", 36) == 0);

    /* prepare-rename */
    rx_buf_clear(&out);
    rx_prepare_rename_answer(&out, R(2, 9, 2, 14), "total");
    same(&out, "2 9 2 14 5\ntotal");
    static char ph[202];
    ph[0] = 'a', ph[1] = '\n';
    memset(ph + 2, 'x', 197);
    ph[199] = (char)0xc3, ph[200] = (char)0xa9;
    rx_buf_clear(&out);
    rx_prepare_rename_answer(&out, R(1, 1, 1, 2), ph);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "1 1 1 2 199\na %.197s", ph + 2);
    same(&out, want.data);
    assert(rx_refuse_answer(&out, "  You cannot\nrename this \n") == -1);
    same(&out, "You cannot rename this");

    /* the resolve answer: edit and command; none; an edit that cannot be sent */
    rx_resolved res;
    memset(&res, 0, sizeof res);
    rx_wedit_file(&res.edit, NULL, 4, 7);
    rx_wedit_edit(&res.edit, R(1, 1, 1, 1), "R", 1);
    rx_command_ref(&res.command, "fix", "z", 1);
    rx_buf_clear(&out);
    assert(rx_resolved_finish(&res, &out) == 0 && res.command.data == NULL);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "%zu 15\n%scommand fix 1\nz", strlen(edit), edit);
    same(&out, want.data);
    rx_buf_clear(&out);
    assert(rx_resolved_finish(&res, &out) == 0);
    same(&out, "0 0\n");
    for (int i = 0; i < 1001; i++) rx_wedit_file(&res.edit, NULL, 4, 7);
    rx_command_ref(&res.command, "fix", "z", 1);
    rx_buf_clear(&out);
    assert(rx_resolved_finish(&res, &out) == -1 && strcmp(res.edit.fail, "toobig") == 0 && out.len == 0);
    rx_wedit_free(&res.edit);
    rx_buf_free(&out);
    rx_buf_free(&want);
}

static int on_declaration(rx_ext *ext, const rx_ask *req, rx_stream *s, rx_locations *out, void *user) {
    (void)ext, (void)s, (void)user;
    rx_location(out, NULL, req->doc, R(1, 1, 1, 2), "d");
    return 1;
}

/* a part first, then the answer */
static int on_implementation(rx_ext *ext, const rx_ask *req, rx_stream *s, rx_locations *out, void *user) {
    (void)ext, (void)user;
    rx_locations part = {{NULL, 0, 0}, 0, 0};
    rx_location(&part, NULL, req->doc, R(1, 1, 1, 2), NULL);
    assert(rx_stream_locations(s, &part) == 0);
    rx_location(out, NULL, req->doc, R(2, 1, 2, 2), NULL);
    return 1;
}

static int on_prepare_rename(rx_ext *ext, const rx_ask *req, rx_buf *out, void *user) {
    (void)ext, (void)user;
    if (req->at.line == 9) return rx_refuse_answer(out, "You cannot rename a keyword");
    if (req->at.line != 8) rx_prepare_rename_answer(out, R(2, 9, 2, 14), "total");
    return 1;
}

static int on_range_format(rx_ext *ext, const rx_range_format_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)user;
    if (req->trim && !req->final_newline && req->tab_width == 4 && req->spaces) {
        rx_wedit_file(out, NULL, req->doc, req->version);
        rx_wedit_edit(out, req->range, "x", 1);
    }
    return 1;
}

static int on_type_format(rx_ext *ext, const rx_type_format_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)user;
    if (strcmp(req->ch, "newline") == 0) {
        rx_range at = {req->at, req->at};
        rx_wedit_file(out, NULL, req->doc, req->version);
        rx_wedit_edit(out, at, "  ", 2);
    }
    return 1;
}

static int on_code_actions(rx_ext *ext, const rx_code_actions_ask *req, rx_code_actions *out, void *user) {
    (void)ext, (void)user;
    assert(strcmp(req->trigger, "invoked") == 0 || strcmp(req->trigger, "auto") == 0);
    assert(req->kinds == out->kinds);
    probe_actions(out);
    return 1;
}

static int on_code_action_resolve(rx_ext *ext, const rx_resolve_ask *req, rx_resolved *out, void *user) {
    (void)ext, (void)user;
    if (req->len == 2 && memcmp(req->data, "no", 2) == 0) return rx_refuse(&out->edit, "The fix\nis gone");
    rx_wedit_file(&out->edit, NULL, req->doc, req->version);
    rx_wedit_edit(&out->edit, R(1, 1, 1, 1), (const char *)req->data, req->len);
    rx_command_ref(&out->command, "fix", "z", 1);
    return 1;
}

/* answers with and makes edits that need the transaction grammar */
static char tx_seen[1024];

static void tx_note(const char *s) {
    assert(strlen(tx_seen) + strlen(s) + 2 < sizeof tx_seen);
    strcat(tx_seen, s);
    strcat(tx_seen, "|");
}

static void on_tx_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    rx_wedit e = {0};
    rx_buf b = {NULL, 0, 0};
    tx_edit(&e, 0);
    tx_note(rx_apply(ext, &e) == 0 ? "ok" : rx_error(ext));
    rx_wedit_free(&e);
    tx_edit(&e, 3);
    assert(rx_propose(ext, "Move", NULL, &e) == -1);
    tx_note(rx_error(ext));
    rx_wedit_free(&e);
    tx_edit(&e, 3);
    if (rx_wedit_finish_for(ext, &e, &b) == 0) {
        rx_buf_add(&b, "", 1);
        tx_note(b.data);
    } else {
        char note[300];
        snprintf(note, sizeof note, "%s %s", e.fail, e.why);
        tx_note(note);
    }
    rx_wedit_free(&e);
    rx_buf_free(&b);
}

static int on_tx_rename(rx_ext *ext, const rx_rename_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)req, (void)user;
    tx_edit(out, 3);
    return 1;
}

static int on_tx_format(rx_ext *ext, const rx_format_ask *req, rx_wedit *out, void *user) {
    (void)ext, (void)req, (void)user;
    tx_edit(out, 1);
    return 1;
}

static int on_tx_actions(rx_ext *ext, const rx_actions_ask *req, rx_actions *out, void *user) {
    (void)ext, (void)req, (void)user;
    rx_wedit e = {0};
    tx_edit(&e, 4);
    rx_action(out, NULL, "Plain", &e, NULL, 0);
    tx_edit(&e, 0);
    rx_action(out, NULL, "Hashed", &e, NULL, 0);
    return 1;
}

static int on_tx_code_actions(rx_ext *ext, const rx_code_actions_ask *req, rx_code_actions *out, void *user) {
    (void)ext, (void)req, (void)user;
    rx_wedit e = {0};
    tx_edit(&e, 4);
    rx_code_action(out, NULL, "Plain", &e, NULL);
    tx_edit(&e, 0);
    rx_code_action(out, NULL, "Hashed", &e, NULL);
    tx_edit(&e, 3);
    rx_code_action(out, NULL, "Move", &e, NULL);
    return 1;
}

static int on_tx_resolve(rx_ext *ext, const rx_resolve_ask *req, rx_resolved *out, void *user) {
    (void)ext, (void)req, (void)user;
    tx_edit(&out->edit, 0);
    return 1;
}

static void tx_rec(rx_buf *want, const char *title, const char *edit) {
    rx_buf_fmt(want, "quickfix %zu %zu 0\n%s%s", strlen(title), strlen(edit), title, edit);
}

/* v1 actions, rename, format, code-actions and code-action-resolve answers, apply, propose and
 * rx_wedit_finish_for follow the features rhun agreed */
static void tx_requests(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_tx_ready;
    h.rename = on_tx_rename;
    h.format = on_tx_format;
    h.actions = on_tx_actions;
    h.code_actions = on_tx_code_actions;
    h.code_action_resolve = on_tx_resolve;
    const char *plain = "1\n2 7 1\n#41 1 1 1 1\ny", *move = "1\nrename 6 6 -\n/p/a.c/p/b.clabel 4\nMove";
    char hashed[64];
    snprintf(hashed, sizeof hashed, "1\n6 fnv1a64:%016llx 1\n/p/a.c1 1 1 1 1\ny", (unsigned long long)rx_fnv1a64("x", 1));
    const char *reqs = "2 actions 4 7 1 1 1 1\n3 rename 4 7 1 1 invoked b\n4 format 4 7 4 yes\n"
                       "5 code-actions 4 7 1 1 1 1 invoked - -\n6 code-action-resolve 4 7 1\nR0 bye\n";
    const char *propose = "unknown propose keeps 12g's grammar, which has no file operations";
    const char *format = "error 4 unknown a format answer keeps 12g's grammar, which has no label\n";
    rx_buf in = {NULL, 0, 0}, got = {NULL, 0, 0}, want = {NULL, 0, 0}, l = {NULL, 0, 0};

    /* without workspace-edits: what needs it is refused, an action with such an edit left out */
    const char *v1 = "actions rename format code-actions";
    rx_buf_fmt(&in, "0 features %s\n0 ready -\n%s", v1, reqs);
    tx_seen[0] = 0;
    run(v1, &h, in.data, &got);
    rx_buf_fmt(&want, "hello 1 %s\n", v1);
    rx_buf_str(&l, "1\n");
    tx_rec(&l, "Plain", plain);
    ok(&want, 2, l.data);
    rx_buf_str(&want, "error 3 unknown file operations need the workspace-edits and file-operations features\n");
    rx_buf_str(&want, format);
    ok(&want, 5, l.data);
    rx_buf_str(&want, "error 6 unknown the edit's fnv1a64 precondition needs the workspace-edits feature, which was not agreed\n");
    same(&got, want.data);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "unknown the edit's fnv1a64 precondition needs the workspace-edits feature, which was not agreed|%s|"
                      "unknown file operations need the workspace-edits and file-operations features|", propose);
    assert(strcmp(tx_seen, want.data) == 0);

    /* with workspace-edits and file-operations: transactions, but format answers and proposals keep
     * 12g's grammar */
    const char *ws = "workspace-edits file-operations actions rename format code-actions";
    rx_buf_clear(&in), rx_buf_clear(&got), rx_buf_clear(&want), rx_buf_clear(&l);
    rx_buf_fmt(&in, "0 features %s\n0 ready -\nok 1\n%s", ws, reqs);
    tx_seen[0] = 0;
    run(ws, &h, in.data, &got);
    rx_buf_fmt(&want, "hello 1 %s\n1 apply %zu\n%s", ws, strlen(hashed), hashed);
    rx_buf_str(&l, "2\n");
    tx_rec(&l, "Plain", plain);
    tx_rec(&l, "Hashed", hashed);
    ok(&want, 2, l.data);
    ok(&want, 3, move);
    rx_buf_str(&want, format);
    rx_buf_clear(&l);
    rx_buf_str(&l, "3\n");
    tx_rec(&l, "Plain", plain);
    tx_rec(&l, "Hashed", hashed);
    tx_rec(&l, "Move", move);
    ok(&want, 5, l.data);
    rx_buf_clear(&l);
    rx_buf_fmt(&l, "%zu 0\n%s", strlen(hashed), hashed);
    ok(&want, 6, l.data);
    same(&got, want.data);
    rx_buf_clear(&want);
    rx_buf_fmt(&want, "ok|%s|%s|", propose, move);
    assert(strcmp(tx_seen, want.data) == 0);
    rx_buf_free(&in), rx_buf_free(&got), rx_buf_free(&want), rx_buf_free(&l);
}

static void actions_requests(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.declaration = on_declaration;
    h.implementation = on_implementation;
    h.prepare_rename = on_prepare_rename;
    h.range_format = on_range_format;
    h.type_format = on_type_format;
    h.code_actions = on_code_actions;
    h.code_action_resolve = on_code_action_resolve;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0};
    const char *feats = "navigation prepare-rename partial-format code-actions query-stream";
    rx_buf in = {NULL, 0, 0};
    rx_buf_fmt(&in, "0 features %s\n0 ready -\n", feats);
    rx_buf_str(&in, "2 declaration 4 7 1 1 invoked -\n3 prepare-rename 4 7 2 10 invoked -\n"
                    "4 prepare-rename 4 7 9 1 invoked -\n5 prepare-rename 4 7 8 1 invoked -\n"
                    "6 range-format 4 7 1 1 2 1 4 yes yes no\n7 type-format 4 7 3 5 newline 4 yes no yes\n"
                    "8 code-actions 4 7 1 1 1 1 invoked quickfix -\n9 code-actions 4 7 1 1 1 1 invoked - d1,d2\n"
                    "10 code-action-resolve 4 7 1\nR11 code-action-resolve 4 7 2\nno"
                    "13 implementation 4 7 1 1 invoked -\nok 1\n14 type-definition 4 7 1 1 invoked -\n"
                    "15 range-format 4 7 1 1 2 1 4 yes yes\n16 type-format 4 7 3 5 newline 0 yes no yes\n"
                    "17 code-actions 4 7 1 1 1 1 later - -\n0 bye\n");
    run(feats, &h, in.data, &got);
    rx_buf_fmt(&want, "hello 1 %s\n", feats);
    ok(&want, 2, "1\n1 1 1 2 2 1\n#4d");
    ok(&want, 3, "2 9 2 14 5\ntotal");
    rx_buf_str(&want, "error 4 refused You cannot rename a keyword\n");
    ok(&want, 5, "");
    ok(&want, 6, "1\n2 7 1\n#41 1 2 1 1\nx");
    ok(&want, 7, "1\n2 7 1\n#43 5 3 5 2\n  ");
    ok(&want, 8, "2\nquickfix 3 0 33\nFixpreferred 0\nresolve 1\nRfixes 2\nd1quickfix 6 0 23\nCannotdisabled 11\nNo fix here");
    ok(&want, 9, go_actions);
    ok(&want, 10, "21 15\n1\n2 7 1\n#41 1 1 1 1\nRcommand fix 1\nz");
    rx_buf_str(&want, "error 11 refused The fix is gone\n1 query-part 13 16\n1\n1 1 1 2 2 0\n#4");
    ok(&want, 13, "1\n2 1 2 2 2 0\n#4");
    rx_buf_str(&want, "error 14 unknown no such request\nerror 15 unknown no such request\n"
                      "error 16 unknown no such request\nerror 17 unknown no such request\n");
    same(&got, want.data);

    /* without the features every request is unknown, and the resolve request's body is read past */
    rx_buf_clear(&got);
    run("rename format", &h,
        "0 features rename format\n2 declaration 4 7 1 1 invoked -\n3 type-definition 4 7 1 1 invoked -\n"
        "4 implementation 4 7 1 1 invoked -\n5 prepare-rename 4 7 2 10 invoked -\n"
        "6 range-format 4 7 1 1 2 1 4 yes yes no\n7 type-format 4 7 3 5 newline 4 yes no yes\n"
        "8 code-actions 4 7 1 1 1 1 invoked - -\n9 code-action-resolve 4 7 1\nR0 bye\n",
        &got);
    rx_buf_clear(&want);
    rx_buf_str(&want, "hello 1 rename format\n");
    for (int id = 2; id <= 9; id++) rx_buf_fmt(&want, "error %d unknown no such request\n", id);
    same(&got, want.data);
    rx_buf_free(&in);
    rx_buf_free(&got);
    rx_buf_free(&want);
}

/* ---- tests (feature "tests") */

static rx_buf t_out;
static char t_log[256];

static void t_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    rx_test_items l;
    memset(&l, 0, sizeof l);
    rx_test_item(&l, "group", "pkg", "pkg", NULL, NULL, "lazy", NULL, NULL);
    int r = rx_test_items_send(ext, "", &l, 1);
    const char *gone[] = {"old"};
    int g = rx_test_remove(ext, gone, 1);
    snprintf(t_log, sizeof t_log, "ready %d %d %s", r, g, r < 0 ? rx_error(ext) : "");
}

static int t_resolve(rx_ext *ext, const char *parent, rx_test_items *out, void *user) {
    (void)ext, (void)user;
    if (!*parent) {
        rx_test_item(out, "group", "pkg", "pkg", NULL, NULL, "lazy", NULL, NULL);
    } else if (strcmp(parent, "pkg") == 0) {
        rx_range r = R(3, 1, 5, 2);
        rx_test_item(out, "test", "pkg/A", "A", "/p/a.c", &r, NULL, "d", NULL);
    } else {
        return rx_test_refuse(out, "no such item");
    }
    return 1;
}

static const char *t_run(rx_ext *ext, const rx_test_run *run, void *user) {
    (void)user;
    if (strcmp(run->profile, "debug") == 0) return "busy the debugger is busy";
    assert(run->run == 1 && run->nitems == 1 && strcmp(run->items[0], "pkg/A") == 0);
    const char *args[] = {"-v"}, *env[] = {"A=1"};
    int64_t ex = rx_test_exec(ext, run->run, "cc", NULL, args, 1, env, 1);
    assert(ex == 9);
    return NULL;
}

static void t_test_out(rx_ext *ext, int64_t exec, int err, const unsigned char *data, size_t len, void *user) {
    (void)ext, (void)user;
    assert(exec == 9 && !err);
    rx_buf_add(&t_out, data, len);
}

static void t_test_exit(rx_ext *ext, int64_t exec, const char *code, void *user) {
    (void)user;
    assert(exec == 9 && strcmp(code, "0") == 0);
    rx_test_results r;
    memset(&r, 0, sizeof r);
    rx_test_result(&r, "pkg/A", "failed", 12);
    rx_test_message(&r, "boom", 4);
    rx_test_location(&r, "/p/a.c", R(4, 5, 4, 5));
    rx_test_expected(&r, "1", 1);
    rx_test_actual(&r, "2", 1);
    rx_test_result(&r, "", NULL, -1);
    rx_test_output(&r, t_out.data, t_out.len);
    assert(rx_test_update(ext, 1, &r) == 0 && r.n == 0);
    rx_test_coverage c;
    memset(&c, 0, sizeof c);
    rx_coverage_file(&c, "/p/a.c");
    rx_coverage_lines(&c, 3, 5, 1);
    assert(rx_test_coverage_send(ext, 1, &c) == 0);
    assert(rx_test_end(ext, 1) == 0);
}

static void t_cancel(rx_ext *ext, int64_t run, void *user) {
    (void)ext, (void)user;
    snprintf(t_log + strlen(t_log), sizeof t_log - strlen(t_log), " cancel %lld", (long long)run);
}

static void test_builders(void) {
    rx_test_items l;
    memset(&l, 0, sizeof l);
    rx_test_item(&l, "test", "a\nb", "x", NULL, NULL, NULL, NULL, NULL);
    rx_buf out = {NULL, 0, 0};
    assert(rx_test_items_finish(&l, "", &out) == -1 && strcmp(l.fail, "syntax") == 0);
    rx_test_items_free(&l);
    rx_range bad = R(2, 5, 2, 4);
    rx_test_item(&l, "test", "a", "x", "/f", &bad, NULL, NULL, NULL);
    assert(l.fail && strcmp(l.fail, "badpos") == 0);
    rx_test_items_free(&l);
    rx_test_item(&l, "suite", "s", "S", NULL, NULL, "nodebug", NULL, "broken");
    assert(rx_test_items_finish(&l, "p", &out) == 0);
    same(&out, "1 1\npsuite 0 0 0 0 nodebug 1 1 0 14\nsSerror 6\nbroken");
    rx_test_items_free(&l);
    rx_buf_free(&out);
    rx_test_results r;
    memset(&r, 0, sizeof r);
    rx_test_result(&r, "", "passed", 1);
    assert(rx_test_results_finish(&r, &out) == -1 && strcmp(r.fail, "syntax") == 0);
    rx_test_results_free(&r);
    rx_test_message(&r, "x", 1);
    assert(r.fail && strcmp(r.why, "an extra before its record") == 0);
    rx_test_results_free(&r);
    rx_test_result(&r, "t", "skipped", -1);
    rx_test_result(&r, "u", "started", 5);
    assert(rx_test_results_finish(&r, &out) == 0);
    same(&out, "2\nskipped - 1 0\ntstarted 5 1 0\nu");
    rx_test_results_free(&r);
    rx_buf_free(&out);
}

static void tests_feature(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = t_ready;
    h.resolve_tests = t_resolve;
    h.run_tests = t_run;
    h.test_out = t_test_out;
    h.test_exit = t_test_exit;
    h.cancel_tests = t_cancel;
    rx_buf got = {NULL, 0, 0}, want = {NULL, 0, 0};
    run("tests", &h,
        "0 features tests\n"
        "0 ready -\n"
        "ok 1\n"
        "ok 2\n"
        "5 tests-resolve 0\n"
        "6 tests-resolve 3\npkg"
        "7 test-run 1 run 1 6\npkg/A\n"
        "0 test-out 9 out 3\nok\n"   /* arrives while test-exec waits: kept for later */
        "ok 3 9\n"
        "0 test-exit 9 0\n"
        "ok 4\n"
        "ok 5\n"
        "ok 6\n"
        "8 test-run 2 debug 0 0\n"
        "0 test-cancel 2\n"
        "9 tests-resolve 1\nx"
        "10 test-run x run 0 0\n"
        "0 bye\n",
        &got);
    const char *roots = "0 1\ngroup 0 0 0 0 lazy 3 3 0 0\npkgpkg";
    const char *kids = "3 1\npkgtest 3 1 5 2 - 5 1 6 8\npkg/AA/p/a.cdesc 1\nd";
    const char *exec = "2 0 1 1\ncc2\n-v3\nA=1";
    const char *update = "2\nfailed 12 5 61\npkg/Amessage 4\nboomlocation 4 5 4 5 6\n/p/a.cexpected 1\n1actual 1\n2"
                         "- - 0 12\noutput 3\nok\n";
    const char *cov = "1\n6 1\n/p/a.c3 5 1\n";
    rx_buf_fmt(&want, "hello 1 tests\n1 test-items replace %zu\n%s", strlen(roots), roots);
    rx_buf_fmt(&want, "2 test-remove 1 4\nold\n");
    rx_buf_fmt(&want, "ok 5 %zu\n%s", strlen(roots), roots);
    rx_buf_fmt(&want, "ok 6 %zu\n%s", strlen(kids), kids);
    rx_buf_fmt(&want, "3 test-exec 1 %zu\n%s", strlen(exec), exec);
    rx_buf_fmt(&want, "ok 7 0\n");
    rx_buf_fmt(&want, "4 test-update 1 %zu\n%s", strlen(update), update);
    rx_buf_fmt(&want, "5 test-coverage 1 %zu\n%s", strlen(cov), cov);
    rx_buf_fmt(&want, "6 test-end 1\n");
    rx_buf_fmt(&want, "error 8 busy the debugger is busy\n");
    rx_buf_fmt(&want, "error 9 refused no such item\n");
    rx_buf_fmt(&want, "error 10 syntax a bad test-run\n");
    if (got.len != want.len || memcmp(got.data, want.data, got.len) != 0)
        fprintf(stderr, "got:\n[%s]\nwant:\n[%s]\n", got.data, want.data);
    same(&got, want.data);
    assert(strcmp(t_log, "ready 0 0  cancel 2") == 0);
    rx_buf_free(&got);
    rx_buf_free(&want);

    /* without agreement: nothing is sent, requests are unknown, Cancel is not delivered */
    t_log[0] = 0;
    run("tests", &h, "0 features\n0 ready -\n3 tests-resolve 0\n0 test-cancel 1\n4 test-run 1 run 0 0\n0 bye\n", &got);
    same(&got, "hello 1 tests\nerror 3 unknown no such request\nerror 4 unknown no such request\n");
    assert(strcmp(t_log, "ready -1 -1 unknown tests was not agreed") == 0);
    rx_buf_free(&got);
    rx_buf_free(&t_out);
}


/* ---- hover-query, selection-ranges, hierarchy, workspace-symbols, document-links,
 * file-diagnostics (go/rhunext/langfeat_test.go, the same bytes) */

static rx_hierarchy_item hitem(const char *name, const char *data) {
    rx_hierarchy_item it;
    memset(&it, 0, sizeof it);
    it.kind = "function", it.name = name, it.detail = "pkg", it.path = "/p/a.go";
    it.range = R(3, 1, 9, 2);
    it.selection = R(3, 6, 3, 6 + (int)strlen(name));
    it.data = data, it.data_len = strlen(data);
    return it;
}

/* an item's record (and its call sites) as the grammar writes it */
static void hitem_rec(rx_buf *b, const char *kind, const char *sel, const char *name, const char *data,
                      const char *const *sites, int nsites) {
    rx_buf_fmt(b, "%s 3 1 9 2 %s 7 %zu 3 %zu", kind, sel, strlen(name), strlen(data));
    if (nsites) rx_buf_fmt(b, " %d", nsites);
    rx_buf_fmt(b, "\n/p/a.go%spkg%s", name, data);
    for (int i = 0; i < nsites; i++) rx_buf_fmt(b, "%s\n", sites[i]);
}

static void langfeat_records(void) {
    rx_buf out = {NULL, 0, 0}, want = {NULL, 0, 0};
    /* file-diagnostics */
    rx_related rel[3];
    memset(rel, 0, sizeof rel);
    rel[0].path = "/p/a.go", rel[0].range = R(1, 1, 1, 4), rel[0].message = "declared\nhere";
    rel[1].path = "relative.go", rel[1].message = "left out";
    rel[2].doc = 3, rel[2].range = R(0, 0, 0, 0);
    rx_diagnostic ds[5];
    memset(ds, 0, sizeof ds);
    char code[151];
    memset(code, 'c', 150), code[150] = 0;
    ds[0].range = R(2, 9, 2, 14), ds[0].severity = "warning", ds[0].message = "unused\ntotal", ds[0].id = "d1";
    ds[0].code = "W1", ds[0].source = "lint", ds[0].href = "https://example.test/W1";
    ds[0].unnecessary = ds[0].deprecated = 1, ds[0].related = rel, ds[0].nrelated = 3;
    ds[1].message = "zero", ds[1].id = "d1";
    ds[2].severity = "error";
    ds[3].severity = "fatal", ds[3].message = "x";
    ds[4].range = R(3, 5, 3, 1), ds[4].severity = "hint", ds[4].message = "back", ds[4].id = "bad id";
    ds[4].href = "ftp://x", ds[4].code = code, ds[4].source = "a\nb";
    int count = 0;
    rx_file_diagnostics_records(ds, 5, &out, &count);
    const char *extra = "id 2\nd1code 2\nW1source 4\nlinthref 23\nhttps://example.test/W1tag unnecessary 0\n"
                        "tag deprecated 0\nrelated 1 1 1 4 7 13\n/p/a.godeclared hererelated 1 1 1 1 2 0\n#3";
    rx_buf_fmt(&want, "2 9 2 14 warning 12 %zu\nunused\ntotal%s", strlen(extra), extra);
    rx_buf_str(&want, "1 1 1 1 error 4 0\nzero");
    rx_buf_fmt(&want, "3 5 3 5 hint 4 %zu\nbackcode 100\n%.100ssource 3\na b", strlen("code 100\n") + 100 + strlen("source 3\na b"), code);
    assert(count == 3);
    same(&out, want.data);
    /* 2000 records at most; the body within 4 MiB */
    rx_diagnostic *many = (rx_diagnostic *)calloc(2001, sizeof *many);
    char *msg = (char *)malloc(5001);
    for (int i = 0; i < 2500; i++) msg[2 * i] = (char)0xc3, msg[2 * i + 1] = (char)0xa9;
    msg[5000] = 0;
    for (int i = 0; i < 2001; i++) many[i].range = R(1, 1, 1, 2), many[i].message = "m";
    rx_buf_clear(&out);
    rx_file_diagnostics_records(many, 2001, &out, &count);
    assert(count == 2000);
    for (int i = 0; i < 2000; i++) many[i].message = msg;
    rx_buf_clear(&out);
    rx_file_diagnostics_records(many, 2000, &out, &count);
    assert(count < 2000 && out.len + 4096 <= ((size_t)4 << 20) && memcmp(out.data, "1 1 1 2 error 4000 0\n", 21) == 0);
    free(many);
    free(msg);

    /* hover */
    rx_buf_clear(&out);
    rx_hover_answer(&out, R(2, 1, 2, 9), "fn pointer", 10);
    same(&out, "2 1 2 9\nfn pointer");
    rx_buf_clear(&out);
    rx_hover_answer(&out, R(2, 1, 2, 9), "", 0);
    same(&out, "");
    char *big = (char *)malloc(80001);
    for (int i = 0; i < 40000; i++) big[2 * i] = (char)0xc3, big[2 * i + 1] = (char)0xa9;
    big[80000] = 0;
    rx_hover_answer(&out, R(8, 5, 8, 1), big, 80000);
    assert(out.len == 8 + (65536 - 8) / 2 * 2 && memcmp(out.data, "0 0 0 0\n", 8) == 0);
    free(big);

    /* selection ranges */
    rx_selections sel;
    memset(&sel, 0, sizeof sel);
    sel.at[0].line = 1, sel.at[0].col = 6, sel.at[1].line = 2, sel.at[1].col = 3, sel.at[2].line = 5, sel.at[2].col = 4;
    sel.npos = 3;
    rx_range g1[] = {R(1, 5, 1, 8), R(1, 5, 1, 8), R(1, 1, 1, 20), R(1, 2, 2, 1), R(1, 1, 9, 1)};
    rx_range g3[] = {R(5, 1, 5, 9), R(5, 9, 5, 1)};
    rx_selection_group(&sel, g1, 5);
    rx_selection_group(&sel, NULL, 0);
    rx_selection_group(&sel, g3, 2);
    rx_buf_clear(&out);
    rx_selections_finish(&sel, &out);
    same(&out, "2\n1 5 1 8\n1 1 1 20\n1\n2 3 2 3\n1\n5 1 5 9\n");
    memset(&sel, 0, sizeof sel);
    sel.npos = 2;
    rx_selection_group(&sel, g3, 2);
    rx_selection_group(&sel, NULL, 0);
    rx_buf_clear(&out);
    rx_selections_finish(&sel, &out);
    same(&out, "1\n5 1 5 9\n");
    memset(&sel, 0, sizeof sel);
    sel.npos = 64;
    rx_range chain[64];
    for (int j = 0; j < 64; j++) chain[j] = R(1000000 - j, 1000 - j, 1000000 + j, 1000 + j);
    for (int i = 0; i < 64; i++) sel.at[i].line = 1000000, sel.at[i].col = 1000, rx_selection_group(&sel, chain, 64);
    rx_buf_clear(&out);
    rx_selections_finish(&sel, &out);
    assert(out.len <= 65536 && memcmp(out.data, "42\n1000000 1000 1000000 1000\n", 29) == 0);

    /* hierarchy */
    rx_hierarchy roots;
    memset(&roots, 0, sizeof roots);
    roots.roots = 1;
    rx_hierarchy_item it = hitem("main", "m1");
    rx_hierarchy_add(&roots, &it);
    it = hitem("", "");
    rx_hierarchy_add(&roots, &it);
    it = hitem("bad", "");
    it.range = R(9, 1, 3, 1);
    rx_hierarchy_add(&roots, &it);
    it = hitem("outside", "o");
    it.selection = R(1, 1, 1, 2);
    rx_hierarchy_add(&roots, &it);
    rx_buf_clear(&out);
    rx_hierarchy_finish(&roots, &out);
    rx_buf_clear(&want);
    rx_buf_str(&want, "2\n");
    hitem_rec(&want, "function", "3 6 3 10", "main", "m1", NULL, 0);
    hitem_rec(&want, "function", "3 1 9 2", "outside", "o", NULL, 0);
    same(&out, want.data);
    roots.roots = 1;
    it = hitem("r", "");
    for (int i = 0; i < 70; i++) rx_hierarchy_add(&roots, &it);
    rx_buf_clear(&out);
    rx_hierarchy_finish(&roots, &out);
    assert(memcmp(out.data, "64\n", 3) == 0);
    rx_calls calls;
    memset(&calls, 0, sizeof calls);
    rx_range sites[] = {R(5, 1, 5, 2), R(6, 2, 6, 1), R(7, 1, 7, 3)};
    it = hitem("late", "");
    rx_call_add(&calls, &it, sites, 3);
    it = hitem("nosites", "");
    rx_call_add(&calls, &it, NULL, 0);
    rx_buf_clear(&out);
    rx_calls_finish(&calls, &out);
    rx_buf_clear(&want);
    rx_buf_str(&want, "1\n");
    const char *const late_sites[] = {"5 1 5 2", "7 1 7 3"};
    hitem_rec(&want, "function", "3 6 3 10", "late", "", late_sites, 2);
    same(&out, want.data);
    rx_range *many_sites = (rx_range *)malloc(1005 * sizeof *many_sites);
    for (int i = 0; i < 1005; i++) many_sites[i] = R(1, 1, 1, 2);
    it = hitem("c", "");
    rx_call_add(&calls, &it, many_sites, 1005);
    rx_buf_clear(&out);
    rx_calls_finish(&calls, &out);
    assert(strstr(out.data, " 1000\n") != NULL);
    free(many_sites);
    rx_hierarchy items;
    memset(&items, 0, sizeof items);
    char *hdata = (char *)malloc(16 * 1024 + 2);
    memset(hdata, 'x', 16 * 1024 + 1), hdata[16 * 1024 + 1] = 0;
    it = hitem("Huge", hdata);
    rx_hierarchy_add(&items, &it);
    it = hitem("Sub", "");
    rx_hierarchy_add(&items, &it);
    rx_buf_clear(&out);
    rx_hierarchy_finish(&items, &out);
    rx_buf_clear(&want);
    rx_buf_str(&want, "1\n");
    hitem_rec(&want, "function", "3 6 3 9", "Sub", "", NULL, 0);
    same(&out, want.data);
    free(hdata);

    /* workspace symbols and links */
    rx_workspace_symbols ws;
    memset(&ws, 0, sizeof ws);
    rx_workspace_symbol(&ws, "/p/a.go", 0, R(3, 6, 3, 10), "function", "Main", "main");
    rx_workspace_symbol(&ws, NULL, 2, R(1, 1, 1, 4), NULL, "mai\nx", NULL);
    rx_workspace_symbol(&ws, "/p/b.go", 0, R(1, 1, 1, 1), "class", "", NULL);
    rx_buf_clear(&out);
    rx_workspace_symbols_finish(&ws, &out);
    same(&out, "2\n3 6 3 10 function 7 4 4\n/p/a.goMainmain1 1 1 4 function 2 5 0\n#2mai x");
    rx_links links;
    memset(&links, 0, sizeof links);
    rx_document_link(&links, R(1, 1, 1, 20), "https://example.test/a b", "spaces");
    rx_document_link(&links, R(1, 1, 1, 20), "https://example.test/a", "Open\nthe page");
    rx_document_link(&links, R(2, 3, 2, 9), "/p/a.go:4:2", NULL);
    rx_document_link(&links, R(3, 3, 3, 9), "/p/a.go:4", NULL);
    rx_document_link(&links, R(4, 3, 4, 9), "#4", NULL);
    rx_document_link(&links, R(5, 3, 5, 9), "relative.go", NULL);
    rx_document_link(&links, R(6, 3, 5, 9), "/p/backwards", NULL);
    rx_document_link(&links, R(7, 1, 7, 4), "ftp://example.test", NULL);
    rx_document_link(&links, R(8, 1, 8, 4), "#0", NULL);
    rx_buf_clear(&out);
    rx_links_finish(&links, &out);
    same(&out, "4\n1 1 1 20 22 13\nhttps://example.test/aOpen the page2 3 2 9 11 0\n/p/a.go:4:2"
               "3 3 3 9 9 0\n/p/a.go:44 3 4 9 2 0\n#4");
    assert(rx_link_target_valid("HTTPS://x.test/\xc3\xa9") && rx_link_target_valid("C:\\p\\a.go:3"));
    assert(rx_link_target_valid("\\\\srv\\share\\a") && rx_link_target_valid("#12"));
    assert(!rx_link_target_valid("http://") && !rx_link_target_valid("#+1") && !rx_link_target_valid("#"));
    assert(!rx_link_target_valid("/a\nb") && !rx_link_target_valid("mailto:a@b") && !rx_link_target_valid(NULL));
    rx_buf_free(&out);
    rx_buf_free(&want);
}

static rx_buf langfeat_seen;

static void on_diagnostics_ready(rx_ext *ext, const char *project, void *user) {
    (void)project, (void)user;
    rx_diagnostic d;
    memset(&d, 0, sizeof d);
    d.range = R(1, 4, 1, 9), d.message = "bad\nname", d.id = "e1";
    if (!rx_has(ext, "file-diagnostics")) {
        int a = rx_file_diagnostics(ext, NULL, 1, 0, &d, 1);
        rx_buf_fmt(&langfeat_seen, "%d %s|", a, rx_error(ext));
        a = rx_reset_diagnostics(ext);
        rx_buf_fmt(&langfeat_seen, "%d %s|", a, rx_error(ext));
        return;
    }
    int a = rx_file_diagnostics(ext, NULL, 1, 0, &d, 1);
    rx_buf_fmt(&langfeat_seen, "%d %s|", a, rx_error(ext));
    a = rx_file_diagnostics(ext, "/p/other.rs", 0, 3, &d, 1);
    rx_buf_fmt(&langfeat_seen, "%d|", a);
    a = rx_file_diagnostics(ext, "other.rs", 0, RX_ON_DISK, &d, 1);
    rx_buf_fmt(&langfeat_seen, "%d %s|", a, rx_error(ext));
    a = rx_file_diagnostics(ext, NULL, 1, RX_ON_DISK, &d, 1);
    rx_buf_fmt(&langfeat_seen, "%d %.6s|", a, rx_error(ext));
    a = rx_reset_diagnostics(ext);
    rx_buf_fmt(&langfeat_seen, "%d|", a);
}

static int on_hover(rx_ext *ext, const rx_ask *req, rx_buf *out, void *user) {
    (void)ext, (void)user;
    char text[32];
    snprintf(text, sizeof text, "fn %s", req->kind);
    if (req->at.line != 9) rx_hover_answer(out, R(2, 1, 2, 9), text, strlen(text));
    return 1;
}

static int on_selection_ranges(rx_ext *ext, const rx_selection_ask *req, rx_selections *out, void *user) {
    (void)ext, (void)user;
    for (int i = 0; i < req->n; i++) {
        int l = req->positions[i].line;
        rx_range g1[] = {R(1, 5, 1, 8), R(1, 5, 1, 8), R(1, 1, 1, 20), R(1, 2, 2, 1)};
        rx_range g[] = {R(l, 1, l, 9), R(l, 9, l, 1)};
        if (l == 1) rx_selection_group(out, g1, 4);
        else if (l == 2) rx_selection_group(out, NULL, 0);
        else rx_selection_group(out, g, 2);
    }
    return 1;
}

static int on_call_hierarchy(rx_ext *ext, const rx_ask *req, rx_hierarchy *out, void *user) {
    (void)ext, (void)req, (void)user;
    assert(out->roots);
    rx_hierarchy_item it = hitem("main", "m1");
    rx_hierarchy_add(out, &it);
    it = hitem("outside", "o");
    it.selection = R(1, 1, 1, 2);
    rx_hierarchy_add(out, &it);
    return 1;
}

static int on_calls_in(rx_ext *ext, const rx_data_ask *req, rx_stream *s, rx_calls *out, void *user) {
    (void)ext, (void)user;
    rx_calls part;
    memset(&part, 0, sizeof part);
    rx_range site = R(4, 2, 4, 6), one = R(1, 1, 1, 2);
    rx_hierarchy_item it = hitem("caller", "c1");
    rx_call_add(&part, &it, &site, 1);
    if (rx_stream_calls(s, &part) < 0) {
        it = hitem("nostream", "");
        rx_call_add(out, &it, &one, 1);
        return 1;
    }
    char name[32];
    snprintf(name, sizeof name, "late%s", (const char *)req->data);
    rx_range sites[] = {R(5, 1, 5, 2), R(6, 2, 6, 1), R(7, 1, 7, 3)};
    it = hitem(name, "");
    rx_call_add(out, &it, sites, 3);
    return 1;
}

static int on_calls_out(rx_ext *ext, const rx_data_ask *req, rx_stream *s, rx_calls *out, void *user) {
    (void)ext, (void)req, (void)s, (void)out, (void)user;
    return 1;
}

static int on_type_hierarchy(rx_ext *ext, const rx_ask *req, rx_hierarchy *out, void *user) {
    (void)ext, (void)req, (void)user;
    rx_hierarchy_item it = hitem("Shape", "t");
    it.kind = "interface";
    rx_hierarchy_add(out, &it);
    return 1;
}

static int on_supertypes(rx_ext *ext, const rx_data_ask *req, rx_stream *s, rx_hierarchy *out, void *user) {
    (void)ext, (void)s, (void)user;
    char name[32];
    snprintf(name, sizeof name, "Base%s", (const char *)req->data);
    rx_hierarchy_item it = hitem(name, "b");
    rx_hierarchy_add(out, &it);
    return 1;
}

static int on_workspace_symbols(rx_ext *ext, const rx_workspace_symbols_ask *req, rx_stream *s,
                                rx_workspace_symbols *out, void *user) {
    (void)ext, (void)user;
    assert(req->max == 500);
    rx_workspace_symbols part;
    memset(&part, 0, sizeof part);
    rx_workspace_symbol(&part, "/p/a.go", 0, R(3, 6, 3, 10), "function", "Main", "main");
    if (rx_stream_workspace_symbols(s, &part) < 0) {
        rx_workspace_symbol(out, "/p/a.go", 0, R(3, 6, 3, 10), "function", "Main", "main");
        return 1;
    }
    char name[300];
    snprintf(name, sizeof name, "%s\nx", req->query);
    rx_workspace_symbol(out, NULL, 2, R(1, 1, 1, 4), NULL, name, NULL);
    return 1;
}

static int on_document_links(rx_ext *ext, const rx_doc_ask *req, rx_links *out, void *user) {
    (void)ext, (void)user;
    char doc[32];
    snprintf(doc, sizeof doc, "#%lld", (long long)req->doc);
    rx_document_link(out, R(1, 1, 1, 20), "https://example.test/a", "Open\nthe page");
    rx_document_link(out, R(2, 3, 2, 9), "/p/a.go:4:2", NULL);
    rx_document_link(out, R(4, 3, 4, 9), doc, NULL);
    rx_document_link(out, R(5, 3, 5, 9), "relative.go", NULL);
    return 1;
}

static void langfeat_requests(void) {
    rx_handlers h;
    memset(&h, 0, sizeof h);
    h.ready = on_diagnostics_ready;
    h.hover = on_hover;
    h.selection_ranges = on_selection_ranges;
    h.call_hierarchy = on_call_hierarchy;
    h.calls_in = on_calls_in;
    h.calls_out = on_calls_out;
    h.type_hierarchy = on_type_hierarchy;
    h.supertypes = on_supertypes;
    h.workspace_symbols = on_workspace_symbols;
    h.document_links = on_document_links;
    const char *feats = "file-diagnostics hover-query selection-ranges hierarchy workspace-symbols document-links query-stream";
    rx_buf in = {NULL, 0, 0}, got = {NULL, 0, 0}, want = {NULL, 0, 0};
    rx_buf_fmt(&in, "0 features %s\n0 ready -\nerror 1 stale 4\nok 2\nok 3\n", feats);
    rx_buf_str(&in, "3 hover-query 4 7 2 5 pointer -\n4 hover-query 4 7 9 1 invoked -\n"
                    "5 selection-ranges 4 7 3 12\n1 6\n2 3\n5 4\n6 call-hierarchy 4 7 3 7 invoked -\n"
                    "7 calls-in 2\nm1ok 4\n8 calls-out 1\nx9 type-hierarchy 4 7 3 7 invoked -\n"
                    "10 supertypes 1\nt11 subtypes 1\nt12 workspace-symbols 500 3\nmaierror 5 cancelled\n"
                    "13 document-links 4 7\n14 selection-ranges 4 7 2 4\n5 4\n0 bye\n");
    run(feats, &h, in.data, &got);
    const char *rec = "1 4 1 9 error 8 7\nbad\nnameid 2\ne1";
    rx_buf_fmt(&want, "hello 1 %s\n", feats);
    rx_buf_fmt(&want, "1 file-diagnostics 0 1 2 %zu\n#1%s", 2 + strlen(rec), rec);
    rx_buf_fmt(&want, "2 file-diagnostics - 1 11 %zu\n/p/other.rs%s", 11 + strlen(rec), rec);
    rx_buf_str(&want, "3 file-diagnostics-reset\n");
    ok(&want, 3, "2 1 2 9\nfn pointer");
    ok(&want, 4, "");
    ok(&want, 5, "2\n1 5 1 8\n1 1 1 20\n1\n2 3 2 3\n1\n5 1 5 9\n");
    rx_buf b = {NULL, 0, 0};
    rx_buf_str(&b, "2\n");
    hitem_rec(&b, "function", "3 6 3 10", "main", "m1", NULL, 0);
    hitem_rec(&b, "function", "3 1 9 2", "outside", "o", NULL, 0);
    ok(&want, 6, b.data);
    rx_buf_clear(&b);
    rx_buf_str(&b, "1\n");
    const char *const site[] = {"4 2 4 6"}, *const late[] = {"5 1 5 2", "7 1 7 3"};
    hitem_rec(&b, "function", "3 6 3 12", "caller", "c1", site, 1);
    rx_buf_fmt(&want, "4 query-part 7 %zu\n%s", b.len, b.data);
    rx_buf_clear(&b);
    rx_buf_str(&b, "1\n");
    hitem_rec(&b, "function", "3 6 3 12", "latem1", "", late, 2);
    ok(&want, 7, b.data);
    ok(&want, 8, "0\n");
    ok(&want, 9, "1\ninterface 3 1 9 2 3 6 3 11 7 5 3 1\n/p/a.goShapepkgt");
    rx_buf_clear(&b);
    rx_buf_str(&b, "1\n");
    hitem_rec(&b, "function", "3 6 3 11", "Baset", "b", NULL, 0);
    ok(&want, 10, b.data);
    rx_buf_str(&want, "error 11 unknown no such request\n");      /* no subtypes handler */
    const char *part = "1\n3 6 3 10 function 7 4 4\n/p/a.goMainmain";
    rx_buf_fmt(&want, "5 query-part 12 %zu\n%s", strlen(part), part);
    ok(&want, 12, part);
    ok(&want, 13, "3\n1 1 1 20 22 13\nhttps://example.test/aOpen the page2 3 2 9 11 0\n/p/a.go:4:24 3 4 9 2 0\n#4");
    rx_buf_str(&want, "error 14 unknown no such request\n");
    same(&got, want.data);
    assert(langfeat_seen.len && strcmp(langfeat_seen.data,
                                       "-1 stale 4|0|-1 syntax a target is an absolute path or an open document|"
                                       "-1 syntax|0|") == 0);
    rx_buf_clear(&langfeat_seen);

    /* without the features: every request is unknown (bodies read past), file-diagnostics is not sent */
    rx_buf_clear(&got);
    run("rename", &h,
        "0 features rename\n0 ready -\n2 hover-query 4 7 2 5 pointer -\n3 selection-ranges 4 7 1 4\n1 6\n"
        "4 call-hierarchy 4 7 3 7 invoked -\n5 calls-in 2\nm16 supertypes 1\nt7 workspace-symbols 500 3\nmai"
        "8 document-links 4 7\n0 bye\n",
        &got);
    rx_buf_clear(&want);
    rx_buf_str(&want, "hello 1 rename\n");
    for (int id = 2; id <= 8; id++) rx_buf_fmt(&want, "error %d unknown no such request\n", id);
    same(&got, want.data);
    assert(strcmp(langfeat_seen.data, "-1 unknown file-diagnostics was not agreed|-1 unknown file-diagnostics was not agreed|") == 0);
    rx_buf_free(&langfeat_seen);
    rx_buf_free(&b);
    rx_buf_free(&in);
    rx_buf_free(&got);
    rx_buf_free(&want);
}
int main(void) {
    retained_patches();
    ai_bodies();
    notes_and_logs();
    snippet_transform_wire();
    snippet_transform_programs();
    snippet_snapshot_decoder();
    prepared_snippets();
    snippet_programs();
    snippet_variants();
    command_removal_guards();
    cursors();
    item_records();
    code_action_records();
    langfeat_records();
    old_tests();
    locations();
    symbols();
    wedits();
    actions();
    cuts();
    transactions();
    tx_parts();
    ordered();
    test_builders();
    dispatch();                 /* last: standard output goes to a file from here on */
    editor_model();
    uri_sides();
    save_actions();
    snippet_requests();
    transform_requests();
    snippet_snapshot_requests();
    event_lengths();
    resources_requests();
    notebooks();
    query_requests();
    actions_requests();
    tx_requests();
    tests_feature();
    langfeat_requests();
    fclose(stdout);
    remove("rhunext-test.in");
    remove("rhunext-test.out");
    fputs("c ok\n", stderr);
    return 0;
}
