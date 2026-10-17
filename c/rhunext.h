/* rhunext.h: write rhun extensions in C or C++. One header, C99, the C library only.
 *
 * An extension is a program rhun starts and talks to over standard input and output, one text
 * line per message (the protocol: v1/v1.md in https://github.com/rhun-editor/extension-protocol).
 * This header does the talking: it reads what rhun sends, calls your handlers, frames your
 * requests and waits for their replies. Events that arrive while a request waits are delivered
 * after the handler returns, in order.
 *
 *     #define RHUNEXT_IMPLEMENTATION      // in exactly one .c or .cpp file
 *     #include "rhunext.h"
 *
 *     static void on_command(rx_ext *ext, const char *name, rx_doc doc, void *user) {
 *         if (strcmp(name, "hello") == 0) rx_toast(ext, "Hello from C");
 *     }
 *     int main(void) {
 *         rx_handlers h = {0};
 *         h.command = on_command;
 *         return rx_run("", &h, NULL);
 *     }
 *
 * Features (section 12 of the spec) are asked for by name in rx_run: "views" for pictures in
 * tabs of their own, "complete" and "signature" for language support ("views complete").
 * Strings in and out are UTF-8; positions are 1-based lines and 1-based byte columns.
 * MIT license. */
#ifndef RHUNEXT_H
#define RHUNEXT_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef int64_t rx_doc;                         /* a document id; 0 is none */
typedef struct { int line, col; } rx_pos;       /* 1-based line, 1-based byte column */
typedef struct { rx_pos start, end; } rx_range; /* half-open */
typedef struct rx_ext rx_ext;

/* bytes that grow: pictures, answers, anything built piece by piece */
typedef struct { char *data; size_t len, cap; } rx_buf;

/* an event, 0 NAME FIELDS: argv the fields after the name, rest the line after the name (a path
 * may hold spaces), body for edits */
typedef struct {
    const char *name;
    int argc;
    char **argv;
    const char *rest;
    const unsigned char *body;
    size_t body_len;
} rx_event;

/* a view's event (12a): kind is size, metrics, hover, down, drag, up, click, leave, wheel, closed */
typedef struct {
    const char *kind;
    double x, y, dy;            /* the pointer; dy for wheel */
    double width, height;       /* size */
    double advance[4], line_height[4];  /* metrics: small, normal, large, mono */
} rx_view_event;

/* what rhun asks for (12b, 12c), an rx_ask: kind is typed, trigger (ch is the character), invoked,
 * incomplete or update */
typedef struct {
    rx_doc doc;
    int64_t version;
    rx_pos at;
    const char *kind;
    const char *ch;
} rx_ask;

typedef struct {
    void (*ready)(rx_ext *ext, const char *project, void *user);    /* project "" without one */
    void (*command)(rx_ext *ext, const char *name, rx_doc doc, void *user);   /* name without "NAME." */
    void (*event)(rx_ext *ext, const rx_event *ev, void *user);     /* every event, first */
    void (*view)(rx_ext *ext, int64_t view, const rx_view_event *ev, void *user);
    /* append the answer's body to out (rx_list_finish, rx_signature_answer); return 1 to answer,
     * 0 for "unknown". An empty signature body hides the help. */
    int (*complete)(rx_ext *ext, const rx_ask *req, rx_buf *out, void *user);
    int (*signature)(rx_ext *ext, const rx_ask *req, rx_buf *out, void *user);
    void (*bye)(rx_ext *ext, void *user);
} rx_handlers;

/* Speaks the protocol on standard input and output until rhun says bye (0) or goes away (1). */
int rx_run(const char *features, const rx_handlers *h, void *user);
/* Whether rhun agreed to a feature. */
int rx_has(rx_ext *ext, const char *feature);

/* Requests. They block until rhun answers: 0 (or an id, a version) on ok, -1 on an error reply,
 * whose "code text" rx_error then says (code: syntax unknown needs nodoc nofile readonly stale
 * badpos toobig busy cancelled refused noview). */
const char *rx_error(rx_ext *ext);
int rx_toast(rx_ext *ext, const char *text);
int rx_status(rx_ext *ext, const char *text);   /* "" clears it */
/* the document's text, malloc'd and NUL-terminated (free() it), or NULL */
unsigned char *rx_text(rx_ext *ext, rx_doc doc, size_t *len, int64_t *version);
int64_t rx_insert(rx_ext *ext, rx_doc doc, int64_t version, const char *text, size_t len);
int64_t rx_replace(rx_ext *ext, rx_doc doc, int64_t version, rx_range r, const char *text, size_t len);
int rx_select(rx_ext *ext, rx_doc doc, rx_range r);
rx_doc rx_open(rx_ext *ext, const char *path, rx_pos at);
int rx_register(rx_ext *ext, const char *name, const char *keys, const char *title);
int rx_cmd(rx_ext *ext, const char *name);
int64_t rx_view(rx_ext *ext, const char *title);        /* feature views: a tab; its id */
int rx_draw(rx_ext *ext, int64_t view, const rx_buf *picture);
int rx_unview(rx_ext *ext, int64_t view);
/* any request: "VERB ARGS" without the id (and with LEN last when there is a body); the reply's
 * fields after "ok ID" go to fields (NUL-terminated), a text/range/ask body to body */
int rx_request(rx_ext *ext, const char *verb_args, const void *body, size_t len, rx_buf *fields, rx_buf *body_out);

void rx_buf_add(rx_buf *b, const void *data, size_t len);
void rx_buf_str(rx_buf *b, const char *s);
void rx_buf_fmt(rx_buf *b, const char *fmt, ...);
void rx_buf_free(rx_buf *b);

/* Pictures (12a), appended to a buffer: logical pixels; colors are theme names (fg muted bg
 * panel border accent error warning success), chart series c1 to c8, or "#rrggbb[aa]". */
void rx_rect(rx_buf *p, double x, double y, double w, double h, const char *color);
void rx_rrect(rx_buf *p, double x, double y, double w, double h, double r, const char *color);
void rx_line(rx_buf *p, double x1, double y1, double x2, double y2, double width, const char *color);
void rx_poly(rx_buf *p, double width, const char *color, const double *xy, int points);
void rx_fill(rx_buf *p, const char *color, const double *xy, int points);
void rx_circle(rx_buf *p, double x, double y, double r, const char *color);
void rx_ring(rx_buf *p, double x, double y, double r, double width, const char *color);
void rx_pie(rx_buf *p, double x, double y, double r, double from, double to, const char *color);
/* size small normal large mono; align left center right; y is the line's top */
void rx_label(rx_buf *p, double x, double y, const char *size, const char *align, const char *color, const char *text);
void rx_clip(rx_buf *p, double x, double y, double w, double h);
void rx_unclip(rx_buf *p);

/* Completion lists (12b): add items, then rx_list_finish writes the body. kind is text method
 * function field variable class struct interface module property enum keyword snippet constant
 * value file folder. insert NULL is the label; detail and doc may be NULL. */
typedef struct { rx_buf items; int n; int more; } rx_list;
void rx_list_item(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                  const char *detail, const char *doc);
/* the same with extra edits made with it (an import), not overlapping r or each other */
void rx_list_item_edits(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                        const char *detail, const char *doc, const rx_range *edits, const char *const *texts, int nedits);
void rx_list_finish(rx_list *l, rx_buf *out);
/* signature help (12c): the active parameter's bytes in the signature, from -1 for none */
void rx_signature_answer(rx_buf *out, const char *signature, int from, int to, const char *doc);

/* the start of the word (letters, digits, _, non-ASCII) that ends at `at` in text */
rx_pos rx_word_start(const unsigned char *text, size_t len, rx_pos at);

#ifdef __cplusplus
}
#endif
#endif /* RHUNEXT_H */

/* ------------------------------------------------------------------------------------------ */
#ifdef RHUNEXT_IMPLEMENTATION

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#endif

#define RX_MAXF 32

typedef struct rx_msg {
    char *line;
    unsigned char *body;
    size_t body_len;
    struct rx_msg *next;
} rx_msg;

struct rx_ext {
    int64_t next_id;
    rx_msg *queue, *queue_tail;     /* messages that came while a request waited */
    char features[256];
    char error[512];
    int gone;
};

void rx_buf_add(rx_buf *b, const void *data, size_t len) {
    if (b->len + len + 1 > b->cap) {
        size_t cap = b->cap ? b->cap * 2 : 256;
        while (cap < b->len + len + 1) cap *= 2;
        char *d = (char *)realloc(b->data, cap);
        if (!d) abort();
        b->data = d;
        b->cap = cap;
    }
    if (len) memcpy(b->data + b->len, data, len);
    b->len += len;
    b->data[b->len] = 0;
}

void rx_buf_str(rx_buf *b, const char *s) { rx_buf_add(b, s, strlen(s)); }

void rx_buf_fmt(rx_buf *b, const char *fmt, ...) {
    char small[512];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(small, sizeof small, fmt, ap);
    va_end(ap);
    if (n < 0) return;
    if ((size_t)n < sizeof small) {
        rx_buf_add(b, small, (size_t)n);
        return;
    }
    char *big = (char *)malloc((size_t)n + 1);
    if (!big) abort();
    va_start(ap, fmt);
    vsnprintf(big, (size_t)n + 1, fmt, ap);
    va_end(ap);
    rx_buf_add(b, big, (size_t)n);
    free(big);
}

void rx_buf_free(rx_buf *b) {
    free(b->data);
    b->data = NULL;
    b->len = b->cap = 0;
}

/* ---- the pipes */

static int rx_read_line(char **out) {
    rx_buf b = {NULL, 0, 0};
    int c;
    while ((c = getchar()) != EOF) {
        if (c == '\n') break;
        char ch = (char)c;
        rx_buf_add(&b, &ch, 1);
    }
    if (c == EOF && b.len == 0) {
        rx_buf_free(&b);
        return 0;
    }
    if (b.len && b.data[b.len - 1] == '\r') b.data[--b.len] = 0;
    if (!b.data) rx_buf_add(&b, "", 0);
    *out = b.data;
    return 1;
}

static unsigned char *rx_read_body(size_t n) {
    unsigned char *body = (unsigned char *)malloc(n + 1);
    if (!body) abort();
    if (n && fread(body, 1, n, stdin) != n) {
        free(body);
        return NULL;
    }
    body[n] = 0;
    return body;
}

static void rx_write(const char *line, const void *body, size_t len) {
    fputs(line, stdout);
    fputc('\n', stdout);
    if (len) fwrite(body, 1, len, stdout);
    fflush(stdout);
}

static int rx_split(char *line, char **f) {
    int n = 0;
    char *p = line;
    while (*p && n < RX_MAXF) {
        while (*p == ' ') p++;
        if (!*p) break;
        f[n++] = p;
        while (*p && *p != ' ') p++;
        if (*p) *p++ = 0;
    }
    return n;
}

/* the last space-separated field of a line, as a number */
static long long rx_last_num(const char *line) {
    const char *s = strrchr(line, ' ');
    return s ? atoll(s + 1) : -1;
}

/* reads the next message: a line, and a body when it is an edits event */
static rx_msg *rx_read_msg(void) {
    char *line;
    if (!rx_read_line(&line)) return NULL;
    rx_msg *m = (rx_msg *)calloc(1, sizeof *m);
    if (!m) abort();
    m->line = line;
    if (strncmp(line, "0 edits ", 8) == 0) {
        long long n = rx_last_num(line);
        if (n < 0 || n > (1 << 30) || !(m->body = rx_read_body((size_t)n))) {
            free(line);
            free(m);
            return NULL;
        }
        m->body_len = (size_t)n;
    }
    return m;
}

static void rx_free_msg(rx_msg *m) {
    free(m->line);
    free(m->body);
    free(m);
}

const char *rx_error(rx_ext *ext) { return ext->error; }

int rx_has(rx_ext *ext, const char *feature) {
    size_t n = strlen(feature);
    const char *p = ext->features;
    while ((p = strstr(p, feature)) != NULL) {
        if ((p == ext->features || p[-1] == ' ') && (p[n] == 0 || p[n] == ' ')) return 1;
        p += n;
    }
    return 0;
}

int rx_request(rx_ext *ext, const char *verb_args, const void *body, size_t len, rx_buf *fields, rx_buf *body_out) {
    if (ext->gone) {
        snprintf(ext->error, sizeof ext->error, "closed rhun went away");
        return -1;
    }
    int64_t id = ++ext->next_id;
    char head[64];
    snprintf(head, sizeof head, "%lld ", (long long)id);
    rx_buf line = {NULL, 0, 0};
    rx_buf_str(&line, head);
    rx_buf_str(&line, verb_args);
    rx_write(line.data, body, len);
    rx_buf_free(&line);
    int wants_body = strncmp(verb_args, "text ", 5) == 0 || strncmp(verb_args, "range ", 6) == 0 ||
                     strncmp(verb_args, "ask ", 4) == 0;
    char want[64];
    snprintf(want, sizeof want, "%lld", (long long)id);
    for (;;) {
        char *l;
        if (!rx_read_line(&l)) {
            ext->gone = 1;
            snprintf(ext->error, sizeof ext->error, "closed rhun went away");
            return -1;
        }
        int is_ok = strncmp(l, "ok ", 3) == 0, is_err = strncmp(l, "error ", 6) == 0;
        if (is_ok || is_err) {
            const char *p = l + (is_ok ? 3 : 6);
            size_t n = strlen(want);
            if (strncmp(p, want, n) == 0 && (p[n] == ' ' || p[n] == 0)) {
                const char *rest = p[n] == ' ' ? p + n + 1 : p + n;
                if (is_err) {
                    snprintf(ext->error, sizeof ext->error, "%s", rest);
                    free(l);
                    return -1;
                }
                if (fields) rx_buf_str(fields, rest);
                if (wants_body) {
                    long long blen = rx_last_num(l);
                    unsigned char *b = blen >= 0 ? rx_read_body((size_t)blen) : NULL;
                    if (!b) {
                        ext->gone = 1;
                        free(l);
                        return -1;
                    }
                    if (body_out) rx_buf_add(body_out, b, (size_t)blen);
                    free(b);
                }
                free(l);
                return 0;
            }
            free(l);        /* a reply to nothing waiting: dropped */
            continue;
        }
        /* an event or a request from rhun: kept for later, in order */
        rx_msg *m = (rx_msg *)calloc(1, sizeof *m);
        if (!m) abort();
        m->line = l;
        if (strncmp(l, "0 edits ", 8) == 0) {
            long long blen = rx_last_num(l);
            if (blen < 0 || !(m->body = rx_read_body((size_t)blen))) {
                ext->gone = 1;
                rx_free_msg(m);
                return -1;
            }
            m->body_len = (size_t)blen;
        }
        if (ext->queue_tail) ext->queue_tail->next = m;
        else ext->queue = m;
        ext->queue_tail = m;
    }
}

static int64_t rx_req_num(rx_ext *ext, const char *verb_args, const void *body, size_t len) {
    rx_buf f = {NULL, 0, 0};
    int r = rx_request(ext, verb_args, body, len, &f, NULL);
    int64_t v = r < 0 ? -1 : (f.data ? atoll(f.data) : 0);
    rx_buf_free(&f);
    return v;
}

static int rx_req_body(rx_ext *ext, const char *verb, const char *text) {
    char line[64];
    size_t n = strlen(text);
    snprintf(line, sizeof line, "%s %zu", verb, n);
    return rx_request(ext, line, text, n, NULL, NULL);
}

int rx_toast(rx_ext *ext, const char *text) { return rx_req_body(ext, "toast", text); }
int rx_status(rx_ext *ext, const char *text) { return rx_req_body(ext, "status", text); }

unsigned char *rx_text(rx_ext *ext, rx_doc doc, size_t *len, int64_t *version) {
    char line[64];
    snprintf(line, sizeof line, "text %lld", (long long)doc);
    rx_buf f = {NULL, 0, 0}, b = {NULL, 0, 0};
    if (rx_request(ext, line, NULL, 0, &f, &b) < 0) {
        rx_buf_free(&f);
        rx_buf_free(&b);
        return NULL;
    }
    if (version) *version = f.data ? atoll(f.data) : 0;
    if (len) *len = b.len;
    rx_buf_free(&f);
    if (!b.data) rx_buf_add(&b, "", 0);
    return (unsigned char *)b.data;
}

int64_t rx_insert(rx_ext *ext, rx_doc doc, int64_t version, const char *text, size_t len) {
    char line[96];
    snprintf(line, sizeof line, "insert %lld %lld %zu", (long long)doc, (long long)version, len);
    return rx_req_num(ext, line, text, len);
}

int64_t rx_replace(rx_ext *ext, rx_doc doc, int64_t version, rx_range r, const char *text, size_t len) {
    char line[160];
    snprintf(line, sizeof line, "replace %lld %lld %d %d %d %d %zu", (long long)doc, (long long)version,
             r.start.line, r.start.col, r.end.line, r.end.col, len);
    return rx_req_num(ext, line, text, len);
}

int rx_select(rx_ext *ext, rx_doc doc, rx_range r) {
    char line[128];
    snprintf(line, sizeof line, "select %lld %d %d %d %d", (long long)doc, r.start.line, r.start.col, r.end.line, r.end.col);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

rx_doc rx_open(rx_ext *ext, const char *path, rx_pos at) {
    rx_buf line = {NULL, 0, 0};
    rx_buf_fmt(&line, "open %d %d %s", at.line, at.col, path);
    rx_doc d = rx_req_num(ext, line.data, NULL, 0);
    rx_buf_free(&line);
    return d;
}

int rx_register(rx_ext *ext, const char *name, const char *keys, const char *title) {
    rx_buf line = {NULL, 0, 0};
    rx_buf_fmt(&line, "register %s %s %s", name, keys && *keys ? keys : "-", title);
    int r = rx_request(ext, line.data, NULL, 0, NULL, NULL);
    rx_buf_free(&line);
    return r;
}

int rx_cmd(rx_ext *ext, const char *name) {
    rx_buf line = {NULL, 0, 0};
    rx_buf_fmt(&line, "cmd %s", name);
    int r = rx_request(ext, line.data, NULL, 0, NULL, NULL);
    rx_buf_free(&line);
    return r;
}

int64_t rx_view(rx_ext *ext, const char *title) {
    rx_buf line = {NULL, 0, 0};
    rx_buf_fmt(&line, "view %s", title);
    int64_t v = rx_req_num(ext, line.data, NULL, 0);
    rx_buf_free(&line);
    return v;
}

int rx_draw(rx_ext *ext, int64_t view, const rx_buf *picture) {
    char line[96];
    size_t n = picture ? picture->len : 0;
    snprintf(line, sizeof line, "draw %lld %zu", (long long)view, n);
    return rx_request(ext, line, n ? picture->data : NULL, n, NULL, NULL);
}

int rx_unview(rx_ext *ext, int64_t view) {
    char line[64];
    snprintf(line, sizeof line, "unview %lld", (long long)view);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

/* ---- pictures */

static void rx_num(rx_buf *b, double v) {
    if (!(v == v)) v = 0;                       /* NaN */
    if (v > 9999999) v = 9999999;
    if (v < -9999999) v = -9999999;
    char s[48];
    snprintf(s, sizeof s, "%.2f", v);
    char *e = s + strlen(s) - 1;
    while (*e == '0') *e-- = 0;
    if (*e == '.') *e = 0;
    if (strcmp(s, "-0") == 0) strcpy(s, "0");
    rx_buf_add(b, " ", 1);
    rx_buf_str(b, s);
}

static double rx_pos0(double v) { return v < 0 ? 0 : v; }

static void rx_color(rx_buf *p, const char *color) {
    rx_buf_add(p, " ", 1);
    rx_buf_str(p, color && *color ? color : "fg");
}

void rx_rect(rx_buf *p, double x, double y, double w, double h, const char *color) {
    rx_buf_str(p, "rect");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(w)), rx_num(p, rx_pos0(h));
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

void rx_rrect(rx_buf *p, double x, double y, double w, double h, double r, const char *color) {
    rx_buf_str(p, "rrect");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(w)), rx_num(p, rx_pos0(h)), rx_num(p, rx_pos0(r));
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

void rx_line(rx_buf *p, double x1, double y1, double x2, double y2, double width, const char *color) {
    rx_buf_str(p, "line");
    rx_num(p, x1), rx_num(p, y1), rx_num(p, x2), rx_num(p, y2), rx_num(p, rx_pos0(width));
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

static void rx_points(rx_buf *p, const double *xy, int points) {
    if (points > 10000) points = 10000;
    for (int i = 0; i < points; i++) rx_num(p, xy[2 * i]), rx_num(p, xy[2 * i + 1]);
}

void rx_poly(rx_buf *p, double width, const char *color, const double *xy, int points) {
    if (points < 2) return;
    rx_buf_str(p, "poly");
    rx_num(p, rx_pos0(width));
    rx_color(p, color);
    rx_points(p, xy, points);
    rx_buf_add(p, "\n", 1);
}

void rx_fill(rx_buf *p, const char *color, const double *xy, int points) {
    if (points < 3) return;
    rx_buf_str(p, "fill");
    rx_color(p, color);
    rx_points(p, xy, points);
    rx_buf_add(p, "\n", 1);
}

void rx_circle(rx_buf *p, double x, double y, double r, const char *color) {
    rx_buf_str(p, "circle");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(r));
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

void rx_ring(rx_buf *p, double x, double y, double r, double width, const char *color) {
    rx_buf_str(p, "ring");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(r)), rx_num(p, rx_pos0(width));
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

void rx_pie(rx_buf *p, double x, double y, double r, double from, double to, const char *color) {
    rx_buf_str(p, "pie");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(r)), rx_num(p, from), rx_num(p, to);
    rx_color(p, color);
    rx_buf_add(p, "\n", 1);
}

void rx_label(rx_buf *p, double x, double y, const char *size, const char *align, const char *color, const char *text) {
    rx_buf_str(p, "text");
    rx_num(p, x), rx_num(p, y);
    rx_buf_fmt(p, " %s %s %s ", size, align, color && *color ? color : "fg");
    for (const char *t = text; *t; t++) {
        char c = (*t == '\n' || *t == '\r') ? ' ' : *t;
        rx_buf_add(p, &c, 1);
    }
    rx_buf_add(p, "\n", 1);
}

void rx_clip(rx_buf *p, double x, double y, double w, double h) {
    rx_buf_str(p, "clip");
    rx_num(p, x), rx_num(p, y), rx_num(p, rx_pos0(w)), rx_num(p, rx_pos0(h));
    rx_buf_add(p, "\n", 1);
}

void rx_unclip(rx_buf *p) { rx_buf_str(p, "unclip\n"); }

/* ---- completion and signature help */

static size_t rx_one_line_len(const char *s) {
    size_t n = 0;
    while (s[n] && s[n] != '\n' && s[n] != '\r') n++;
    return n;
}

void rx_list_item_edits(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                        const char *detail, const char *doc, const rx_range *edits, const char *const *texts, int nedits) {
    rx_buf extra = {NULL, 0, 0};
    for (int i = 0; i < nedits; i++) {
        size_t n = strlen(texts[i]);
        rx_buf_fmt(&extra, "edit %d %d %d %d %zu\n", edits[i].start.line, edits[i].start.col,
                   edits[i].end.line, edits[i].end.col, n);
        rx_buf_add(&extra, texts[i], n);
    }
    if (doc && *doc) {
        size_t n = strlen(doc);
        rx_buf_fmt(&extra, "doc %zu\n", n);
        rx_buf_add(&extra, doc, n);
    }
    if (!insert) insert = label;
    if (!detail) detail = "";
    size_t ll = rx_one_line_len(label), il = strlen(insert), dl = rx_one_line_len(detail);
    size_t size = 64 + ll + il + dl + extra.len;
    if (l->n >= 4096 || l->items.len + size > (1u << 20) - 64) {
        l->more = 1;                    /* past the caps: typing asks again */
        rx_buf_free(&extra);
        return;
    }
    rx_buf_fmt(&l->items, "%d %d %d %d %s %zu %zu %zu %zu\n", r.start.line, r.start.col, r.end.line, r.end.col,
               kind && *kind ? kind : "text", ll, il, dl, extra.len);
    rx_buf_add(&l->items, label, ll);
    rx_buf_add(&l->items, insert, il);
    rx_buf_add(&l->items, detail, dl);
    rx_buf_add(&l->items, extra.data, extra.len);
    l->n++;
    rx_buf_free(&extra);
}

void rx_list_item(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                  const char *detail, const char *doc) {
    rx_list_item_edits(l, r, kind, label, insert, detail, doc, NULL, NULL, 0);
}

void rx_list_finish(rx_list *l, rx_buf *out) {
    rx_buf_fmt(out, "%d %d\n", l->n, l->more ? 1 : 0);
    rx_buf_add(out, l->items.data, l->items.len);
    rx_buf_free(&l->items);
    l->n = l->more = 0;
}

void rx_signature_answer(rx_buf *out, const char *signature, int from, int to, const char *doc) {
    size_t sl = rx_one_line_len(signature);
    if (from < 0 || to < from || (size_t)to > sl) from = to = -1;
    rx_buf_fmt(out, "%d %d\n", from, to);
    rx_buf_add(out, signature, sl);
    rx_buf_add(out, "\n", 1);
    if (doc) rx_buf_str(out, doc);
}

rx_pos rx_word_start(const unsigned char *text, size_t len, rx_pos at) {
    size_t off = 0;
    for (int line = 1; line < at.line; line++) {
        const unsigned char *nl = (const unsigned char *)memchr(text + off, '\n', len - off);
        if (!nl) return at;
        off = (size_t)(nl - text) + 1;
    }
    size_t end = off + (size_t)(at.col - 1), s;
    if (end > len) end = len;
    for (s = end; s > off; s--) {
        unsigned char c = text[s - 1];
        if (!(c == '_' || c >= 0x80 || (c >= '0' && c <= '9') || ((c | 0x20) >= 'a' && (c | 0x20) <= 'z'))) break;
    }
    rx_pos p = {at.line, (int)(s - off) + 1};
    return p;
}

/* ---- the loop */

static void rx_answer(rx_ext *ext, const rx_handlers *h, void *user, char **f, int n) {
    /* ID VERB DOC VERSION LINE COL KIND CHAR */
    int ok = 0;
    rx_buf body = {NULL, 0, 0};
    if (n >= 8) {
        rx_ask req;
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.at.line = atoi(f[4]);
        req.at.col = atoi(f[5]);
        req.kind = f[6];
        req.ch = strcmp(f[6], "trigger") == 0 ? f[7] : "";
        if (strcmp(f[1], "complete") == 0 && h->complete) ok = h->complete(ext, &req, &body, user);
        else if (strcmp(f[1], "signature") == 0 && h->signature) ok = h->signature(ext, &req, &body, user);
    }
    char line[96];
    if (ok) {
        snprintf(line, sizeof line, "ok %s %zu", f[0], body.len);
        rx_write(line, body.data, body.len);
    } else {
        snprintf(line, sizeof line, "error %s unknown no such request", f[0]);
        rx_write(line, NULL, 0);
    }
    rx_buf_free(&body);
}

static void rx_view_dispatch(rx_ext *ext, const rx_handlers *h, void *user, char **f, int n) {
    if (n < 4) return;
    rx_view_event ev;
    memset(&ev, 0, sizeof ev);
    ev.kind = f[3];
    double v[16] = {0};
    for (int i = 4; i < n && i - 4 < 16; i++) v[i - 4] = atof(f[i]);
    if (strcmp(ev.kind, "size") == 0) {
        ev.width = v[0], ev.height = v[1];
    } else if (strcmp(ev.kind, "metrics") == 0) {
        static const char *names[4] = {"small", "normal", "large", "mono"};
        for (int i = 4; i + 2 < n; i += 3)
            for (int k = 0; k < 4; k++)
                if (strcmp(f[i], names[k]) == 0) ev.advance[k] = atof(f[i + 1]), ev.line_height[k] = atof(f[i + 2]);
    } else {
        ev.x = v[0], ev.y = v[1], ev.dy = v[2];
    }
    h->view(ext, atoll(f[2]), &ev, user);
}

int rx_run(const char *features, const rx_handlers *h, void *user) {
#ifdef _WIN32
    _setmode(_fileno(stdin), _O_BINARY);
    _setmode(_fileno(stdout), _O_BINARY);
#endif
    static rx_ext ext_s;
    rx_ext *ext = &ext_s;
    memset(ext, 0, sizeof *ext);
    char hello[300];
    snprintf(hello, sizeof hello, "hello 1%s%s", features && *features ? " " : "", features ? features : "");
    rx_write(hello, NULL, 0);
    for (;;) {
        rx_msg *m = ext->queue;
        if (m) {
            ext->queue = m->next;
            if (!ext->queue) ext->queue_tail = NULL;
        } else if (!(m = rx_read_msg())) {
            return 1;
        }
        char *copy = (char *)malloc(strlen(m->line) + 1);
        if (!copy) abort();
        strcpy(copy, m->line);
        char *f[RX_MAXF];
        int n = rx_split(copy, f);
        int done = 0;
        if (n >= 2 && strcmp(f[0], "0") == 0) {
            rx_event ev;
            ev.name = f[1];
            ev.argc = n - 2;
            ev.argv = f + 2;
            const char *rest = strchr(m->line + 2, ' ');
            ev.rest = rest ? rest + 1 : "";
            ev.body = m->body;
            ev.body_len = m->body_len;
            if (strcmp(f[1], "features") == 0) snprintf(ext->features, sizeof ext->features, "%s", ev.rest);
            if (h->event) h->event(ext, &ev, user);
            if (strcmp(f[1], "ready") == 0 && h->ready) {
                h->ready(ext, strcmp(ev.rest, "-") == 0 ? "" : ev.rest, user);
            } else if (strcmp(f[1], "run") == 0 && n >= 4 && h->command) {
                const char *dot = strchr(f[2], '.');
                h->command(ext, dot ? dot + 1 : f[2], atoll(f[3]), user);
            } else if (strcmp(f[1], "view") == 0 && h->view) {
                rx_view_dispatch(ext, h, user, f, n);
            } else if (strcmp(f[1], "bye") == 0) {
                if (h->bye) h->bye(ext, user);
                done = 1;
            }
        } else if (n >= 2 && f[0][0] >= '1' && f[0][0] <= '9') {
            rx_answer(ext, h, user, f, n);
        }
        free(copy);
        rx_free_msg(m);
        if (done) return 0;
    }
}

#endif /* RHUNEXT_IMPLEMENTATION */
