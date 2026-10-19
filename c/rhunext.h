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
 * tabs of their own; "complete", "signature", "definition", "references", "symbols", "actions",
 * "rename" and "format" for language support ("views complete definition").
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
struct rx_commit_message_ask_s;                  /* source control: rx_commit_message_ask */
struct rx_quickdiff_ask_s;                       /* and rx_quickdiff_ask */

/* bytes that grow: pictures, answers, anything built piece by piece */
typedef struct { char *data; size_t len, cap; } rx_buf;

/* an event, 0 NAME FIELDS: argv the fields after the name, rest the line after the name (a path
 * may hold spaces), body for edits and action */
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

/* what rhun asks for (12b to 12e), an rx_ask: kind is typed, trigger (ch is the character), invoked,
 * incomplete or update */
typedef struct {
    int64_t id;                 /* rhun's id for it: a "cancel" event names it */
    rx_doc doc;
    int64_t version;            /* positions in the answer are in the document at this version */
    rx_pos at;
    const char *kind;
    const char *ch;
} rx_ask;

/* code actions (12f): for the selection, or an empty range at the cursor */
typedef struct {
    int64_t id;
    rx_doc doc;
    int64_t version;
    rx_range range;
} rx_actions_ask;

/* rename (12g): the symbol at `at` becomes name (no spaces) */
typedef struct {
    int64_t id;
    rx_doc doc;
    int64_t version;
    rx_pos at;
    const char *name;
} rx_rename_ask;

/* format (12h), with the document's own settings: spaces 1 indents with spaces, 0 with tabs */
typedef struct {
    int64_t id;
    rx_doc doc;
    int64_t version;
    int tab_width, spaces;
} rx_format_ask;

/* Source actions before saving (1.1). A cancel event names id. Serial handlers must bound
 * their work; custom asynchronous dispatchers must revoke that invocation on cancellation. */
typedef struct {
    int64_t id;
    rx_doc doc;
    int64_t version;
    const char *kind;
} rx_save_actions_ask;

/* answers to 12d to 12h, built with rx_location, rx_symbol, rx_action and rx_wedit_file (below);
 * the fields are the builders' own */
typedef struct { rx_buf recs; int n, full; } rx_locations;
typedef struct { rx_buf recs; int n, full; } rx_symbols;
typedef struct { rx_buf recs; int n, full; } rx_actions;
typedef struct {
    rx_buf body, group, target;     /* the files so far; the open file's edits and PATH */
    int64_t version;
    int files, edits, group_edits, open;
    const char *fail;               /* NULL, or the code it is refused with (toobig, refused, */
    char why[256];                  /* syntax) and why, one line */
} rx_wedit;

/* Notebooks (feature "notebooks"). nb-open: the file's path and bytes (len 0 for a new file);
 * nb-save: the path and the notebook body; nb-run: run source for (nb, cell, run). */
typedef struct rx_nb_open { int64_t id, nb; const char *path; const unsigned char *data; size_t len; } rx_nb_open;
typedef struct rx_nb_save { int64_t id, nb; const char *path; const unsigned char *body; size_t len; } rx_nb_save;
typedef struct rx_nb_run { int64_t nb, cell, run; const char *lang; const unsigned char *source; size_t len; } rx_nb_run;

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
    /* Definition, references, symbols (12d, 12e): add to out (rx_location, rx_symbol); return 1 to
     * answer (none says there is none), 0 for "unknown". */
    int (*definition)(rx_ext *ext, const rx_ask *req, rx_locations *out, void *user);
    int (*references)(rx_ext *ext, const rx_ask *req, rx_locations *out, void *user);
    int (*symbols)(rx_ext *ext, const rx_ask *req, rx_symbols *out, void *user);
    /* Code actions (12f): add to out (rx_action) and return the same; the user choosing one that
     * has data sends it to action (data[len] is 0), after its edit. */
    int (*actions)(rx_ext *ext, const rx_actions_ask *req, rx_actions *out, void *user);
    void (*action)(rx_ext *ext, const unsigned char *data, size_t len, void *user);
    /* Rename, format (12g, 12h): build the workspace edit in out (rx_wedit_file, rx_wedit_edit) and
     * return the same; an empty one changes nothing. Nothing can be renamed there: return
     * rx_refuse(out, why). An edit past the limits is answered toobig. */
    int (*rename)(rx_ext *ext, const rx_rename_ask *req, rx_wedit *out, void *user);
    int (*format)(rx_ext *ext, const rx_format_ask *req, rx_wedit *out, void *user);
    int (*save_actions)(rx_ext *ext, const rx_save_actions_ask *req, rx_wedit *out, void *user);
    /* Notebooks (feature "notebooks", v1.1): nb_open appends a notebook body to out (rx_nb_begin,
     * rx_nb_cell, rx_nb_output, rx_nb_item) and returns NULL, or returns why it cannot (shown in
     * the tab); nb_save reads req->body (rx_nb_read_begin...) and appends the file's bytes to out,
     * or returns why not. nb_run starts a run (answer with rx_nb_state, rx_nb_output_send,
     * rx_nb_stream); nb_event hears "interrupt", "restart", "kernel" and "closed". */
    const char *(*nb_open)(rx_ext *ext, const rx_nb_open *req, rx_buf *out, void *user);
    const char *(*nb_save)(rx_ext *ext, const rx_nb_save *req, rx_buf *out, void *user);
    void (*nb_run)(rx_ext *ext, const rx_nb_run *run, void *user);
    void (*nb_event)(rx_ext *ext, const char *what, int64_t nb, void *user);
    /* Commit messages (feature "commit-message", [provides] commit_message = yes): append the
     * message to out and return 1; append why not and return -1 (the user sees it); 0 for
     * "unknown". req's strings live until it returns. */
    int (*commit_message)(rx_ext *ext, const struct rx_commit_message_ask_s *req, rx_buf *out, void *user);
    /* Quick-diff bases (feature "quickdiff", [provides] quickdiff = PATTERNS): append the file's
     * base text to out (nothing: leave the gutter marks to git) and return 1; 0 for "unknown". */
    int (*quickdiff)(rx_ext *ext, const struct rx_quickdiff_ask_s *req, rx_buf *out, void *user);
    /* Notebooks: a kernel started with rx_nb_exec ended; code its exit status (128 + N after
     * signal N), or -1 when rhun ended it. */
    void (*nb_exec_exit)(rx_ext *ext, int64_t exec, int code, void *user);
} rx_handlers;

/* Speaks the protocol on standard input and output until rhun says bye (0) or goes away (1). */
int rx_run(const char *features, const rx_handlers *h, void *user);
/* Whether rhun agreed to a feature. */
int rx_has(rx_ext *ext, const char *feature);

/* Requests. They block until rhun answers: 0 (or an id, a version) on ok, -1 on an error reply,
 * whose "code text" rx_error then says (code: syntax unknown needs nodoc nofile readonly stale
 * badpos toobig busy cancelled refused noview), for the log rather than for the user. */
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
/* Protocol 1.1, negotiated "commands": remove an own runtime command and its shortcut. */
int rx_unregister(rx_ext *ext, const char *name);
/* Declarations after rx_unregister. Coalesced invalidations always need a rescan. */
int64_t rx_watch_workspace(rx_ext *ext);
int rx_unwatch_workspace(rx_ext *ext, int64_t watch);
int rx_parse_workspace_watch(const rx_event *event, int64_t *watch, int *rescan);
/* Source control (protocol 1.1). Feature "git-data" with [needs] git = read: rhun's Git data.
 * Requests return 0 on ok (-1 and rx_error otherwise); results own their body: free them with
 * their _free function. Strings point into the body and are not NUL-terminated (use the lengths).
 * Feature "commit-message": rhun asks the commit_message handler for a message (below). */
typedef struct {
    rx_buf body;
    const char *root, *branch, *upstream;   /* work tree path; branch (7 hex digits when detached); "origin/main" or "" */
    size_t root_len, branch_len, upstream_len;
    char head[12];                          /* branch, detached, unborn */
    char state[12];                         /* clean, dirty, merging, rebasing */
    int64_t ahead, behind;
} rx_git_repo_info;
typedef struct { char x, y; const char *path; size_t path_len; } rx_git_status_entry;   /* '.' an unchanged side */
typedef struct { rx_buf body; rx_git_status_entry *entries; size_t n; } rx_git_status_list;
typedef struct {
    char sha[72];
    int64_t time;                           /* author time, seconds since 1970 */
    const char *author, *mail, *summary;
    size_t author_len, mail_len, summary_len;
} rx_blame_commit;
typedef struct { char sha[72]; int64_t l1, l2, orig; } rx_blame_run;   /* lines l1..l2 from commit sha (all 0s: not committed) */
typedef struct { rx_buf body; rx_blame_commit *commits; size_t ncommits; rx_blame_run *runs; size_t nruns; } rx_blame;
typedef struct {
    char sha[72];
    const char *parents;                    /* SHAs joined by ',', "" for a root commit */
    size_t parents_len;
    int64_t time;
    const char *author, *subject;
    size_t author_len, subject_len;
} rx_log_entry;
typedef struct { rx_buf body; rx_log_entry *entries; size_t n; } rx_log;
int rx_git_repo(rx_ext *ext, rx_git_repo_info *out);
int rx_git_status(rx_ext *ext, rx_git_status_list *out);
int rx_git_blame(rx_ext *ext, rx_doc doc, rx_blame *out);
/* path NULL: the history of HEAD; l1 = l2 = 0: the file's commits; else its lines l1..l2 at HEAD */
int rx_git_log(rx_ext *ext, const char *path, int l1, int l2, int max, rx_log *out);
int rx_git_show(rx_ext *ext, const char *rev, const char *path, rx_buf *out);     /* the file's bytes at rev */
int rx_git_diff_open(rx_ext *ext, const char *rev1, const char *rev2, const char *path);  /* rev2 NULL: the work tree */
/* decoders of the bodies (out->body already filled): 0, or -1 for a malformed body */
int rx_parse_git_repo(rx_git_repo_info *out);
int rx_parse_git_status(rx_git_status_list *out);
int rx_parse_blame(rx_blame *out);
int rx_parse_log(rx_log *out);
void rx_git_repo_free(rx_git_repo_info *r);
void rx_git_status_free(rx_git_status_list *s);
void rx_blame_free(rx_blame *b);
void rx_log_free(rx_log *l);
int rx_is_git_changed(const rx_event *event);       /* "0 git changed": ask again */
int rx_valid_rev(const char *rev);
/* rhun's checks of a commit message: trims spaces, tabs and line breaks (start and length in
 * *start, *len); 0 when it is 1 to 8192 bytes without control characters but tab, CR, LF */
int rx_check_commit_message(const char *m, size_t n, size_t *start, size_t *len);
/* what commit_message gets: a cancel event names id */
typedef struct rx_commit_message_ask_s { int64_t id; const char *branch, *diff; size_t branch_len, diff_len; } rx_commit_message_ask;
int rx_parse_commit_message(int64_t id, const unsigned char *body, size_t len, rx_commit_message_ask *out);
/* what quickdiff gets; rx_quickdiff_refresh asks rhun to ask again for a file (NULL: every file) */
typedef struct rx_quickdiff_ask_s { int64_t id; rx_doc doc; int64_t version; } rx_quickdiff_ask;
int rx_quickdiff_refresh(rx_ext *ext, const char *path);


int rx_cmd(rx_ext *ext, const char *name);
int64_t rx_view(rx_ext *ext, const char *title);        /* feature views: a tab; its id */
int rx_draw(rx_ext *ext, int64_t view, const rx_buf *picture);
int rx_unview(rx_ext *ext, int64_t view);
/* a workspace edit (12g; documents write) across documents and files: checked whole, then each
 * document changed as one undo step, files that are not open opened in tabs, unsaved; on an error
 * nothing changes. The edit is used up; one that cannot be sent (past the limits) fails as toobig
 * without being sent. For a language server's workspace/applyEdit. */
int rx_apply(rx_ext *ext, rx_wedit *edit);
/* Negotiated save-actions: an intermediate edit for this active save invocation. The edit is
 * used up on every path. Returns the credited native version, or -1 on failure. */
int64_t rx_save_apply(rx_ext *ext, int64_t request_id, rx_doc doc, int64_t version, rx_wedit *edit);
/* Prepared UTF-8 text, with byte offsets and an earlier primary parent (one-based, 0 root).
 * First positive group occurrence is primary, repetitions are mirrors, group 0 the final selection (empty for a caret).
 * Text/fields are borrowed only for the duration of each call. */
typedef struct { int64_t group; size_t start, end, parent; } rx_snippet_field;
typedef struct {
    const char *text;
    size_t len;
    const rx_snippet_field *fields;
    size_t nfields;
} rx_snippet;
/* Option bytes may be empty or contain newlines and NUL. Storage is borrowed until encoding. */
typedef struct { const char *text; size_t len; } rx_snippet_option;
typedef struct { int64_t group; const rx_snippet_option *options; size_t noptions; } rx_snippet_choice;
/* Compiled program bytes are borrowed only during validation or encoding. */
typedef struct {
    const void *regex;
    size_t regex_len;
    const void *replacement;
    size_t replacement_len;
} rx_snippet_transform_program;
typedef struct { size_t field; rx_snippet_transform_program transform; } rx_snippet_field_transform;
typedef struct {
    const rx_snippet *snippet;
    const rx_snippet_choice *choices;
    size_t nchoices;
    /* Borrowed one-based nonprimary field indices with unequal initial text. */
    const size_t *initial_variants;
    size_t ninitial_variants;
    const rx_snippet_field_transform *transforms;
    size_t ntransforms;
} rx_snippet_program;
/* part: 0 arguments/combined size, 1 regex, 2 replacement. Outputs are optional.
 * error_offset is program-local, zero on success or an argument/size error. */
int rx_snippet_transform_program_validate(const rx_snippet_transform_program *program, int *part, size_t *error_offset);
/* Validate canonical compiled SNTX bindings and nested programs without allocation or
 * execution. Does not check field existence/ownership. error_offset is optional;
 * it receives zero on success, otherwise the absolute first invalid byte. */
int rx_snippet_transform_validate(const void *bytes, size_t len, size_t *error_offset);
int rx_snippet_program_validate(const rx_snippet_program *program);
int rx_snippet_program_encode(const rx_snippet_program *program, rx_buf *out);
int64_t rx_insert_snippet_program(rx_ext *ext, rx_doc doc, int64_t version, rx_range range, const rx_snippet_program *program);

/* Owned read-only insertion context. Free a successful result before reusing it.
 * Strings preserve their byte lengths, including NUL. Never includes the clipboard. */
typedef struct {
    int64_t version;
    rx_pos position;
    int tab_width, insert_spaces, crlf;
    rx_buf selected_text, current_line, path, workspace, indent, line_comment;
} rx_snippet_snapshot;
void rx_snippet_snapshot_free(rx_snippet_snapshot *snapshot);
int rx_get_snippet_snapshot(rx_ext *ext, rx_doc doc, int64_t version, rx_range range, rx_snippet_snapshot *out);
int rx_snippet_validate(const rx_snippet *snippet);
/* Append an explicit snippet record, or return -1 without changing out for invalid input. */
int rx_snippet_encode(const rx_snippet *snippet, rx_buf *out);
int64_t rx_insert_snippet(rx_ext *ext, rx_doc doc, int64_t version, rx_range range, const rx_snippet *snippet);
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

/* Completion lists (12b): start from a zeroed rx_list (rx_list l = {0}; in C++ rx_list l{};), add
 * items, then rx_list_finish writes the body; with no items it is the empty list. Set l.more = 1
 * when you cut the list short: typing in the word then asks again (it is set for you past 4096
 * items or 1 MB). Give every candidate for the word: rhun filters and orders by what is typed.
 * kind is text method function field variable class struct interface module property enum
 * keyword snippet constant value file folder. insert NULL is the label; detail and doc may be
 * NULL. */
typedef struct { rx_buf items; int n; int more; } rx_list;
/* Empty prepared text stays empty. Invalid field metadata falls back to plain prepared text.
 * Additional edits and documentation follow the ordinary completion API. */
void rx_list_item_snippet_program(rx_list *list, rx_range range, const char *kind, const char *label,
                         const rx_snippet_program *program, const char *detail, const char *doc,
                         const rx_range *edits, const char *const *texts, int nedits);
void rx_list_item_snippet(rx_list *list, rx_range range, const char *kind, const char *label,
                         const rx_snippet *snippet, const char *detail, const char *doc,
                         const rx_range *edits, const char *const *texts, int nedits);
void rx_list_item(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                  const char *detail, const char *doc);
/* the same with extra edits made with it (an import), not overlapping r or each other */
void rx_list_item_edits(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                        const char *detail, const char *doc, const rx_range *edits, const char *const *texts, int nedits);
/* the same with the cursor cursor bytes into insert when the item is chosen (a snippet's $0); -1,
 * or an offset past insert or inside a character, leaves it after insert */
void rx_list_item_cursor(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                         const char *detail, const char *doc, const rx_range *edits, const char *const *texts,
                         int nedits, long cursor);
void rx_list_finish(rx_list *l, rx_buf *out);
/* signature help (12c): the active parameter's bytes in the signature, from -1 for none */
void rx_signature_answer(rx_buf *out, const char *signature, int from, int to, const char *doc);

/* Definition and references (12d): a location is a file by its absolute path, or with path NULL
 * the open document doc (an untitled one), and a range there (in the open document when the file
 * is open, else in the file on disk); preview is its line's text, trimmed (NULL for none). One
 * location opens at once, several make a list. Past 10000 or 1 MB the rest are left out.
 * rhun drops a whole answer for one field past its cap (12d), so the builders cut names, titles,
 * details and previews to 1000 bytes and kind words to 40, at a character; a symbol or action whose
 * name or title is then empty, and a location whose path is over 4096 bytes, are left out. */
void rx_location(rx_locations *l, const char *path, rx_doc doc, rx_range r, const char *preview);
/* Symbols (12e), in document order. kind is file module namespace package class method property
 * field constructor enum interface function variable constant string number boolean array object
 * key null enum_member struct event operator type_parameter (NULL is function); depth is 0 at the
 * top level, one more for each level it is nested in; detail (a signature, a type) may be NULL.
 * Past 10000 or 1 MB the rest are left out. */
void rx_symbol(rx_symbols *l, rx_range r, const char *kind, int depth, const char *name, const char *detail);
/* A workspace edit (12g): start from a zeroed rx_wedit (rx_wedit e = {0}; in C++ rx_wedit e{};),
 * begin each file's edits with rx_wedit_file, then add them. They are made against one text, the
 * open document at version or with RX_ON_DISK the file on disk (rhun opens it), do not overlap,
 * and insertions at one position keep their order. rhun checks the whole edit before it changes
 * anything, and saves nothing. Past 4 MB, 1000 files or 100000 edits, or with a path over 4096
 * bytes, it cannot be sent (an edit cut short would leave files half changed): none of it is. */
#define RX_ON_DISK (-1)
void rx_wedit_file(rx_wedit *e, const char *path, rx_doc doc, int64_t version);
void rx_wedit_edit(rx_wedit *e, rx_range r, const char *text, size_t len);
/* nothing can be renamed there, and why (shown to the user): return rx_refuse(out, why); */
int rx_refuse(rx_wedit *e, const char *why);
/* Code actions (12f): kind quickfix (listed first), refactor, source or another word (NULL is
 * quickfix); title one line (an empty one leaves the action out); edit (NULL for none) is applied
 * when the action is chosen, and is used up here (one that cannot be sent leaves the action out);
 * data, len bytes of anything, comes back to the action handler. Past 1000 actions or 4 MB the rest
 * are left out. */
void rx_action(rx_actions *l, const char *kind, const char *title, rx_wedit *edit, const void *data, size_t len);
/* The bodies, appended to out, for answers built outside a handler; each empties its builder.
 * rx_wedit_finish returns 0, or -1 when the edit cannot be sent (e->fail and e->why say why) and
 * then appends nothing. rx_wedit_free drops an edit that is not finished. */
void rx_locations_finish(rx_locations *l, rx_buf *out);
void rx_symbols_finish(rx_symbols *l, rx_buf *out);
void rx_actions_finish(rx_actions *l, rx_buf *out);
int rx_wedit_finish(rx_wedit *e, rx_buf *out);
void rx_wedit_free(rx_wedit *e);

/* Writing a notebook body: rx_nb_begin with the number of cells, then each cell with the number of
 * its outputs, each output with the number of its items, each item. kind "code" or "markup"; lang a
 * grammar name ("-" none); count -1 for none; output kind stdout stderr result display error; id
 * NULL for none; item type text ansi png jpeg gif (w h the logical size, 0 0 natural). */
void rx_nb_begin(rx_buf *out, int cells, const void *meta, size_t meta_len);
void rx_nb_cell(rx_buf *out, const char *kind, const char *lang, int64_t count, const void *source,
                size_t source_len, const void *meta, size_t meta_len, int outputs);
void rx_nb_output(rx_buf *out, const char *kind, const char *id, const void *keep, size_t keep_len, int items);
void rx_nb_item(rx_buf *out, const char *type, int w, int h, const void *data, size_t len);
/* Reading one (nb-save's): each returns 1, or 0 on a body that cannot be read. */
typedef struct { const unsigned char *p, *end; int cells; const unsigned char *meta; size_t meta_len; } rx_nb_reader;
typedef struct { char kind[8]; char lang[130]; int64_t count; const unsigned char *source; size_t source_len;
                 const unsigned char *meta; size_t meta_len; int outputs; } rx_nb_cell_rec;
typedef struct { char kind[8]; char id[72]; const unsigned char *keep; size_t keep_len; int items; } rx_nb_output_rec;
typedef struct { char type[72]; int w, h; const unsigned char *data; size_t len; } rx_nb_item_rec;
int rx_nb_read_begin(rx_nb_reader *r, const unsigned char *body, size_t len);
int rx_nb_read_cell(rx_nb_reader *r, rx_nb_cell_rec *c);
int rx_nb_read_output(rx_nb_reader *r, rx_nb_output_rec *o);
int rx_nb_read_item(rx_nb_reader *r, rx_nb_item_rec *it);
/* A run's state (queued running done failed cancelled) and count (-1 leaves it); an output body
 * (built with rx_nb_output and rx_nb_item; op append, replace with target an index or an id, clear
 * and clear-wait with no body); stream text (stderr 0 or 1, cut in 64 KB messages); the kernel
 * button's text and state (idle busy starting dead none). 0 or -1 as the other requests. */
int rx_nb_state(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, const char *state, int64_t count);
int rx_nb_output_send(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, const char *op, const char *target,
                      const void *body, size_t len);
int rx_nb_stream(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, int is_stderr, const void *data, size_t len);
int rx_nb_kernel_label(rx_ext *ext, int64_t nb, const char *state, const char *label);
/* A kernel for notebook nb, started as the user outside the sandbox (rhun asks the user the first
 * time in a project, so this may wait): program a name of [needs] kernels or an absolute path
 * whose last part is one, dir "" for the notebook's folder, args and NAME=VALUE env. Returns its
 * EXEC (its output goes to kernel-EXEC.log in $RHUN_STATE), or -1. rx_nb_exec_signal sends
 * "interrupt", "terminate" or "kill" to its process group. */
int64_t rx_nb_exec(rx_ext *ext, int64_t nb, const char *program, const char *dir, const char *const *args, int argc,
                   const char *const *env, int envc);
int rx_nb_exec_signal(rx_ext *ext, int64_t exec, const char *signal);
/* The command body of rx_nb_exec (and test-exec), onto out. */
void rx_command_body(rx_buf *out, const char *program, const char *dir, const char *const *args, int argc,
                     const char *const *env, int envc);

/* the start of the word (letters, digits, _, non-ASCII) that ends at `at` in text */
rx_pos rx_word_start(const unsigned char *text, size_t len, rx_pos at);
/* the rest of an event's line after its first n fields: rx_event_tail(ev, 3) is the path of
 * "opened DOC VERSION LANG PATH" (a path may hold spaces, which argv splits) */
const char *rx_event_tail(const rx_event *ev, int n);

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
#define RX_BODY_MAX ((long long)1 << 30)     /* the longest reply body read (text, range, ask) */
#define RX_EVENT_BODY_MAX ((long long)4 << 20)  /* an edits or action event's: rhun sends less */

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
        /* bytes from the buffer itself move with it */
        const char *src = (const char *)data;
        int inside = b->data && src >= b->data && src < b->data + b->cap;
        size_t at = inside ? (size_t)(src - b->data) : 0;
        size_t cap = b->cap ? b->cap * 2 : 256;
        while (cap < b->len + len + 1) cap *= 2;
        char *d = (char *)realloc(b->data, cap);
        if (!d) abort();
        b->data = d;
        b->cap = cap;
        if (inside) data = d + at;
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
    if (n > (size_t)RX_BODY_MAX) return NULL;
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

/* the last space-separated field of a line as a body's length: a plain decimal of 1 to 10 digits
 * and at most max, else -1 (checked before anything is allocated) */
static long long rx_body_len(const char *line, long long max) {
    const char *s = strrchr(line, ' ');
    if (!s || !s[1] || strlen(s + 1) > 10) return -1;
    long long v = 0;
    for (s++; *s; s++) {
        if (*s < '0' || *s > '9') return -1;
        v = v * 10 + (*s - '0');
    }
    return v <= max ? v : -1;
}

/* the session is over: requests fail with why, and rx_run returns 1 once the queue is empty */
static void rx_gone(rx_ext *ext, const char *why) {
    ext->gone = 1;
    snprintf(ext->error, sizeof ext->error, "closed %s", why);
}

static const char rx_bad_len[] = "a body's length that is not a decimal of at most 10 digits within its cap";

/* the events followed by a body, LEN their last field: edits (6a) and action (12f) */
static int rx_has_body(const char *line) {
    if (strncmp(line, "0 edits ", 8) == 0 || strncmp(line, "0 action ", 9) == 0 ||
        strncmp(line, "0 nb-run ", 9) == 0) return 1;
    /* ID commit-message LEN */
    if (line[0] < '1' || line[0] > '9') return 0;
    const char *sp = strchr(line, ' ');
    return sp && strncmp(sp + 1, "commit-message ", 15) == 0;
}

/* a request from rhun followed by a body: nb-open and nb-save (ID VERB NB LEN), up to 64 MB */
static int rx_host_body(const char *line) {
    const char *sp = strchr(line, ' ');
    if (!sp || line[0] < '1' || line[0] > '9') return 0;
    return strncmp(sp + 1, "nb-open ", 8) == 0 || strncmp(sp + 1, "nb-save ", 8) == 0;
}
#define RX_NB_BODY_MAX (((long long)64 << 20) + 65536)

/* reads the next message: a line, and a body when it is an edits or action event; NULL when the
 * session is over (end of input, or a length rhun cannot have sent: its body is not read) */
static rx_msg *rx_read_msg(rx_ext *ext) {
    char *line;
    if (!rx_read_line(&line)) {
        rx_gone(ext, "rhun went away");
        return NULL;
    }
    rx_msg *m = (rx_msg *)calloc(1, sizeof *m);
    if (!m) abort();
    m->line = line;
    if (rx_has_body(line) || rx_host_body(line)) {
        long long n = rx_body_len(line, rx_host_body(line) ? RX_NB_BODY_MAX : RX_EVENT_BODY_MAX);
        if (n < 0 || !(m->body = rx_read_body((size_t)n))) {
            rx_gone(ext, n < 0 ? rx_bad_len : "rhun went away");
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
    /* ids are 1 to 2147483647 (section 3); requests here wait one at a time, so wrapping is safe */
    if (ext->next_id >= 2147483647) ext->next_id = 0;
    int64_t id = ++ext->next_id;
    char head[64];
    snprintf(head, sizeof head, "%lld ", (long long)id);
    rx_buf line = {NULL, 0, 0};
    rx_buf_str(&line, head);
    rx_buf_str(&line, verb_args);
    rx_write(line.data, body, len);
    rx_buf_free(&line);
    int context_body = strncmp(verb_args, "snippet-context ", 16) == 0;
    int wants_body = strncmp(verb_args, "text ", 5) == 0 || strncmp(verb_args, "range ", 6) == 0 ||
                     strncmp(verb_args, "ask ", 4) == 0 || context_body ||
                     strcmp(verb_args, "git-repo") == 0 || strcmp(verb_args, "git-status") == 0 ||
                     strncmp(verb_args, "git-blame ", 10) == 0 || strncmp(verb_args, "git-log ", 8) == 0 ||
                     strncmp(verb_args, "git-show ", 9) == 0;
    char want[64];
    snprintf(want, sizeof want, "%lld", (long long)id);
    for (;;) {
        char *l;
        if (!rx_read_line(&l)) {
            rx_gone(ext, "rhun went away");
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
                    long long blen = rx_body_len(l, context_body ? (4u << 20) : RX_BODY_MAX);
                    unsigned char *b = blen >= 0 ? rx_read_body((size_t)blen) : NULL;
                    if (!b) {
                        rx_gone(ext, blen < 0 ? rx_bad_len : "rhun went away");
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
        if (rx_has_body(l) || rx_host_body(l)) {
            long long blen = rx_body_len(l, rx_host_body(l) ? RX_NB_BODY_MAX : RX_EVENT_BODY_MAX);
            if (blen < 0 || !(m->body = rx_read_body((size_t)blen))) {
                rx_gone(ext, blen < 0 ? rx_bad_len : "rhun went away");
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

int rx_unregister(rx_ext *ext, const char *name) {
    if (!rx_has(ext, "commands")) {
        snprintf(ext->error, sizeof ext->error, "unknown commands feature was not agreed");
        return -1;
    }
    size_t n = name ? strlen(name) : 0;
    if (!n || n > 40 || strspn(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != n) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid command name");
        return -1;
    }
    char line[64];
    snprintf(line, sizeof line, "unregister %s", name);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

static int64_t rx_watch_id(const char *raw) {
    if (!raw || !*raw) return 0;
    uint64_t value = 0;
    size_t count = 0;
    while (*raw) {
        unsigned char c = (unsigned char)*raw++;
        if (c < '0' || c > '9' || ++count > 10) return 0;
        value = value * 10 + c - '0';
        if (value > 2147483647u) return 0;
    }
    return (int64_t)value;
}

int64_t rx_watch_workspace(rx_ext *ext) {
    if (!rx_has(ext, "workspace_watch")) {
        snprintf(ext->error, sizeof ext->error, "unknown workspace_watch was not negotiated");
        return -1;
    }
    rx_buf fields = {NULL, 0, 0};
    int result = rx_request(ext, "watch-workspace", NULL, 0, &fields, NULL);
    int64_t watch = result < 0 ? 0 : rx_watch_id(fields.data);
    rx_buf_free(&fields);
    if (result < 0) return -1;
    if (!watch) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid workspace watch handle");
        return -1;
    }
    return watch;
}

int rx_unwatch_workspace(rx_ext *ext, int64_t watch) {
    if (!rx_has(ext, "workspace_watch") || watch <= 0 || watch > 2147483647) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid workspace watch");
        return -1;
    }
    char line[64];
    snprintf(line, sizeof line, "unwatch-workspace %lld", (long long)watch);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

int rx_parse_workspace_watch(const rx_event *event, int64_t *watch, int *rescan) {
    if (!event || !event->name || strcmp(event->name, "watch") || event->argc != 2 ||
        !event->argv || !event->argv[1] || !watch || !rescan) return 0;
    int64_t id = rx_watch_id(event->argv[0]);
    if (!id) return 0;
    if (!strcmp(event->argv[1], "changed")) *rescan = 0;
    else if (!strcmp(event->argv[1], "rescan")) *rescan = 1;
    else return 0;
    *watch = id;
    return 1;
}

/* ---- source control (protocol 1.1): git-data and commit-message ---- */

/* a reader of length-framed bodies: header words, LF, then raw bytes */
typedef struct { const char *b; size_t len, pos; int bad; } rx_fr;

static const char *rx_fr_word(rx_fr *r, size_t *n) {
    if (r->pos < r->len && r->b[r->pos] == ' ') r->pos++;
    size_t s = r->pos;
    while (r->pos < r->len && r->b[r->pos] != ' ' && r->b[r->pos] != '\n') r->pos++;
    *n = r->pos - s;
    if (!*n) r->bad = 1;
    return r->b + s;
}
static int64_t rx_fr_num(rx_fr *r) {
    size_t n;
    const char *w = rx_fr_word(r, &n);
    if (!n || n > 18) { r->bad = 1; return 0; }
    int64_t v = 0;
    for (size_t i = 0; i < n; i++) {
        if (w[i] < '0' || w[i] > '9') { r->bad = 1; return 0; }
        v = v * 10 + (w[i] - '0');
    }
    return v;
}
static void rx_fr_eol(rx_fr *r) {
    if (r->pos < r->len && r->b[r->pos] == '\n') r->pos++;
    else r->bad = 1;
}
static const char *rx_fr_bytes(rx_fr *r, int64_t n) {
    if (r->bad || n < 0 || (uint64_t)n > r->len - r->pos) { r->bad = 1; return ""; }
    const char *p = r->b + r->pos;
    r->pos += (size_t)n;
    return p;
}
static void rx_fr_sha(rx_fr *r, char out[72]) {
    size_t n;
    const char *w = rx_fr_word(r, &n);
    if (n >= 72) { r->bad = 1; n = 0; }
    memcpy(out, w, n);
    out[n] = 0;
}
static int rx_fr_done(const rx_fr *r) { return !r->bad && r->pos == r->len ? 0 : -1; }

static int rx_git_ok(rx_ext *ext) {
    if (rx_has(ext, "git-data")) return 1;
    snprintf(ext->error, sizeof ext->error, "unknown git-data was not negotiated");
    return 0;
}

int rx_valid_rev(const char *rev) {
    size_t n = rev ? strlen(rev) : 0;
    if (!n || n > 200 || rev[0] == '-') return 0;
    for (size_t i = 0; i < n; i++) {
        char c = rev[i];
        if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strchr("._/~^@{}+-", c))) return 0;
    }
    return 1;
}

int rx_parse_git_repo(rx_git_repo_info *o) {
    rx_fr r = {o->body.data, o->body.len, 0, 0};
    int64_t rl = rx_fr_num(&r), bl = rx_fr_num(&r), ul = rx_fr_num(&r);
    size_t n;
    const char *w = rx_fr_word(&r, &n);
    if (n >= sizeof o->head) return -1;
    memcpy(o->head, w, n), o->head[n] = 0;
    o->ahead = rx_fr_num(&r), o->behind = rx_fr_num(&r);
    w = rx_fr_word(&r, &n);
    if (n >= sizeof o->state) return -1;
    memcpy(o->state, w, n), o->state[n] = 0;
    rx_fr_eol(&r);
    o->root = rx_fr_bytes(&r, rl), o->root_len = (size_t)rl;
    o->branch = rx_fr_bytes(&r, bl), o->branch_len = (size_t)bl;
    o->upstream = rx_fr_bytes(&r, ul), o->upstream_len = (size_t)ul;
    if (strcmp(o->head, "branch") && strcmp(o->head, "detached") && strcmp(o->head, "unborn")) return -1;
    if (strcmp(o->state, "clean") && strcmp(o->state, "dirty") && strcmp(o->state, "merging") && strcmp(o->state, "rebasing")) return -1;
    return rx_fr_done(&r);
}

int rx_parse_git_status(rx_git_status_list *o) {
    rx_fr r = {o->body.data, o->body.len, 0, 0};
    int64_t n = rx_fr_num(&r);
    rx_fr_eol(&r);
    if (r.bad || n < 0 || (uint64_t)n > r.len) return -1;
    o->entries = (rx_git_status_entry *)calloc((size_t)n + 1, sizeof *o->entries);
    if (!o->entries) abort();
    for (o->n = 0; (int64_t)o->n < n && !r.bad; o->n++) {
        size_t xl;
        const char *xy = rx_fr_word(&r, &xl);
        int64_t pl = rx_fr_num(&r);
        rx_fr_eol(&r);
        if (xl != 2) return -1;
        rx_git_status_entry *e = &o->entries[o->n];
        e->x = xy[0], e->y = xy[1];
        e->path = rx_fr_bytes(&r, pl), e->path_len = (size_t)pl;
    }
    return rx_fr_done(&r);
}

int rx_parse_blame(rx_blame *o) {
    rx_fr r = {o->body.data, o->body.len, 0, 0};
    int64_t nc = rx_fr_num(&r), nr = rx_fr_num(&r);
    rx_fr_eol(&r);
    if (r.bad || (uint64_t)nc > r.len || (uint64_t)nr > r.len) return -1;
    o->commits = (rx_blame_commit *)calloc((size_t)nc + 1, sizeof *o->commits);
    o->runs = (rx_blame_run *)calloc((size_t)nr + 1, sizeof *o->runs);
    if (!o->commits || !o->runs) abort();
    for (o->ncommits = 0; (int64_t)o->ncommits < nc && !r.bad; o->ncommits++) {
        rx_blame_commit *c = &o->commits[o->ncommits];
        rx_fr_sha(&r, c->sha);
        c->time = rx_fr_num(&r);
        int64_t al = rx_fr_num(&r), ml = rx_fr_num(&r), sl = rx_fr_num(&r);
        rx_fr_eol(&r);
        c->author = rx_fr_bytes(&r, al), c->author_len = (size_t)al;
        c->mail = rx_fr_bytes(&r, ml), c->mail_len = (size_t)ml;
        c->summary = rx_fr_bytes(&r, sl), c->summary_len = (size_t)sl;
    }
    for (o->nruns = 0; (int64_t)o->nruns < nr && !r.bad; o->nruns++) {
        rx_blame_run *u = &o->runs[o->nruns];
        rx_fr_sha(&r, u->sha);
        u->l1 = rx_fr_num(&r), u->l2 = rx_fr_num(&r), u->orig = rx_fr_num(&r);
        rx_fr_eol(&r);
    }
    return rx_fr_done(&r);
}

int rx_parse_log(rx_log *o) {
    rx_fr r = {o->body.data, o->body.len, 0, 0};
    int64_t n = rx_fr_num(&r);
    rx_fr_eol(&r);
    if (r.bad || (uint64_t)n > r.len) return -1;
    o->entries = (rx_log_entry *)calloc((size_t)n + 1, sizeof *o->entries);
    if (!o->entries) abort();
    for (o->n = 0; (int64_t)o->n < n && !r.bad; o->n++) {
        rx_log_entry *e = &o->entries[o->n];
        rx_fr_sha(&r, e->sha);
        size_t pl;
        const char *p = rx_fr_word(&r, &pl);
        if (pl == 1 && p[0] == '-') pl = 0;
        e->parents = p, e->parents_len = pl;
        e->time = rx_fr_num(&r);
        int64_t al = rx_fr_num(&r), sl = rx_fr_num(&r);
        rx_fr_eol(&r);
        e->author = rx_fr_bytes(&r, al), e->author_len = (size_t)al;
        e->subject = rx_fr_bytes(&r, sl), e->subject_len = (size_t)sl;
    }
    return rx_fr_done(&r);
}

int rx_parse_commit_message(int64_t id, const unsigned char *body, size_t len, rx_commit_message_ask *o) {
    rx_fr r = {(const char *)(body ? body : (const unsigned char *)""), len, 0, 0};
    int64_t bl = rx_fr_num(&r), dl = rx_fr_num(&r);
    rx_fr_eol(&r);
    o->id = id;
    o->branch = rx_fr_bytes(&r, bl), o->branch_len = (size_t)bl;
    o->diff = rx_fr_bytes(&r, dl), o->diff_len = (size_t)dl;
    return rx_fr_done(&r);
}

int rx_check_commit_message(const char *m, size_t n, size_t *start, size_t *len) {
    size_t a = 0, b = n;
    while (a < b && (m[a] == ' ' || m[a] == '\t' || m[a] == '\r' || m[a] == '\n')) a++;
    while (b > a && (m[b - 1] == ' ' || m[b - 1] == '\t' || m[b - 1] == '\r' || m[b - 1] == '\n')) b--;
    *start = a, *len = b - a;
    if (b == a || b - a > 8192) return -1;
    for (size_t i = a; i < b; i++) {
        unsigned char c = (unsigned char)m[i];
        if (c == 0x7f || (c < 0x20 && c != '\t' && c != '\r' && c != '\n')) return -1;
    }
    return 0;
}

int rx_is_git_changed(const rx_event *ev) {
    return ev && ev->name && strcmp(ev->name, "git") == 0 && ev->argc == 1 && strcmp(ev->argv[0], "changed") == 0;
}

/* a git-data request whose body (when ok) goes to body, decoded by parse */
static int rx_git_req(rx_ext *ext, const char *line, const char *path, rx_buf *body) {
    if (!rx_git_ok(ext)) return -1;
    return rx_request(ext, line, path, path ? strlen(path) : 0, NULL, body);
}

int rx_git_repo(rx_ext *ext, rx_git_repo_info *out) {
    memset(out, 0, sizeof *out);
    if (rx_git_req(ext, "git-repo", NULL, &out->body) < 0) return -1;
    if (rx_parse_git_repo(out) == 0) return 0;
    snprintf(ext->error, sizeof ext->error, "syntax invalid git-repo reply");
    return -1;
}

int rx_git_status(rx_ext *ext, rx_git_status_list *out) {
    memset(out, 0, sizeof *out);
    if (rx_git_req(ext, "git-status", NULL, &out->body) < 0) return -1;
    if (rx_parse_git_status(out) == 0) return 0;
    snprintf(ext->error, sizeof ext->error, "syntax invalid git-status reply");
    return -1;
}

int rx_git_blame(rx_ext *ext, rx_doc doc, rx_blame *out) {
    memset(out, 0, sizeof *out);
    char line[64];
    snprintf(line, sizeof line, "git-blame %lld", (long long)doc);
    if (rx_git_req(ext, line, NULL, &out->body) < 0) return -1;
    if (rx_parse_blame(out) == 0) return 0;
    snprintf(ext->error, sizeof ext->error, "syntax invalid git-blame reply");
    return -1;
}

int rx_git_log(rx_ext *ext, const char *path, int l1, int l2, int max, rx_log *out) {
    memset(out, 0, sizeof *out);
    char line[96];
    snprintf(line, sizeof line, "git-log %d %d %d %zu", max, l1, l2, path ? strlen(path) : (size_t)0);
    if (rx_git_req(ext, line, path, &out->body) < 0) return -1;
    if (rx_parse_log(out) == 0) return 0;
    snprintf(ext->error, sizeof ext->error, "syntax invalid git-log reply");
    return -1;
}

int rx_git_show(rx_ext *ext, const char *rev, const char *path, rx_buf *out) {
    if (!rx_valid_rev(rev) || !path) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid revision");
        return -1;
    }
    char line[256];
    snprintf(line, sizeof line, "git-show %s %zu", rev, strlen(path));
    return rx_git_req(ext, line, path, out);
}

int rx_git_diff_open(rx_ext *ext, const char *rev1, const char *rev2, const char *path) {
    if (!rev2) rev2 = "-";
    if (!rx_valid_rev(rev1) || (strcmp(rev2, "-") && !rx_valid_rev(rev2)) || !path) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid revision");
        return -1;
    }
    char line[512];
    snprintf(line, sizeof line, "git-diff-open %s %s %zu", rev1, rev2, strlen(path));
    if (!rx_git_ok(ext)) return -1;
    return rx_request(ext, line, path, strlen(path), NULL, NULL);
}

void rx_git_repo_free(rx_git_repo_info *r) { rx_buf_free(&r->body); memset(r, 0, sizeof *r); }
void rx_git_status_free(rx_git_status_list *s) { free(s->entries); rx_buf_free(&s->body); memset(s, 0, sizeof *s); }
void rx_blame_free(rx_blame *b) { free(b->commits); free(b->runs); rx_buf_free(&b->body); memset(b, 0, sizeof *b); }
void rx_log_free(rx_log *l) { free(l->entries); rx_buf_free(&l->body); memset(l, 0, sizeof *l); }

int rx_quickdiff_refresh(rx_ext *ext, const char *path) {
    if (!rx_has(ext, "quickdiff")) {
        snprintf(ext->error, sizeof ext->error, "unknown quickdiff was not negotiated");
        return -1;
    }
    if (!path) path = "-";
    char line[64];
    snprintf(line, sizeof line, "quickdiff-refresh %zu", strlen(path));
    return rx_request(ext, line, path, strlen(path), NULL, NULL);
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

int rx_apply(rx_ext *ext, rx_wedit *edit) {
    rx_buf body = {NULL, 0, 0};
    if (rx_wedit_finish(edit, &body) < 0) {
        snprintf(ext->error, sizeof ext->error, "%s %s", edit->fail, edit->why);
        rx_wedit_free(edit);
        return -1;
    }
    char line[64];
    snprintf(line, sizeof line, "apply %zu", body.len);
    int r = rx_request(ext, line, body.data, body.len, NULL, NULL);
    rx_buf_free(&body);
    return r;
}

/* Decimal fields with overflow checks, including identities received from the host. */
static int rx_positive_num(const char *s, int64_t max, int64_t *out) {
    int64_t value = 0;
    if (!s || !*s) return 0;
    for (; *s; s++) {
        if (*s < '0' || *s > '9' || value > (max - (*s - '0')) / 10) return 0;
        value = value * 10 + (*s - '0');
    }
    *out = value;
    return 1;
}

int64_t rx_save_apply(rx_ext *ext, int64_t request_id, rx_doc doc, int64_t version, rx_wedit *edit) {
    rx_buf body = {NULL, 0, 0}, fields = {NULL, 0, 0};
    int64_t result = -1;
    const char *code = NULL, *why = NULL;
    if (!rx_has(ext, "save-actions")) { code = "unknown"; why = "save-actions feature was not agreed"; }
    else if (request_id <= 0 || request_id > 2147483647 || doc <= 0 || version < 0) {
        code = "syntax"; why = "invalid save-action identity";
    }
    if (code) {
        snprintf(ext->error, sizeof ext->error, "%s %s", code, why);
        rx_wedit_free(edit);
        return -1;
    }
    if (rx_wedit_finish(edit, &body) < 0) {
        snprintf(ext->error, sizeof ext->error, "%s %s", edit->fail, edit->why);
        rx_wedit_free(edit);
    } else {
        char line[128];
        snprintf(line, sizeof line, "save-apply %lld %lld %lld %zu", (long long)request_id, (long long)doc, (long long)version, body.len);
        if (rx_request(ext, line, body.data, body.len, &fields, NULL) == 0 &&
            (!rx_positive_num(fields.data, INT64_MAX, &result) || result < version)) {
            snprintf(ext->error, sizeof ext->error, "syntax invalid save-apply reply");
            result = -1;
        }
    }
    rx_buf_free(&body);
    rx_buf_free(&fields);
    return result;
}

static int rx_snippet_utf8(const unsigned char *s, size_t n) {
    size_t at = 0;
    while (at < n) {
        unsigned char c = s[at++];
        size_t more;
        if (c < 0x80) continue;
        if (c >= 0xc2 && c <= 0xdf) more = 1;
        else if (c >= 0xe0 && c <= 0xef) more = 2;
        else if (c >= 0xf0 && c <= 0xf4) more = 3;
        else return 0;
        if (more > n - at) return 0;
        if ((c == 0xe0 && s[at] < 0xa0) || (c == 0xed && s[at] >= 0xa0) ||
            (c == 0xf0 && s[at] < 0x90) || (c == 0xf4 && s[at] >= 0x90)) return 0;
        for (size_t j = 0; j < more; j++) if ((s[at++] & 0xc0) != 0x80) return 0;
    }
    return 1;
}

/* Portable compiled programs. SIZE_MAX denotes success for private readers. */
static uint32_t rx_st_word(const unsigned char *p, size_t at) {
    return (uint32_t)p[at] | ((uint32_t)p[at+1] << 8) |
        ((uint32_t)p[at+2] << 16) | ((uint32_t)p[at+3] << 24);
}
static int rx_st_scalar(uint32_t n) { return n <= 0x10ffff && !(n >= 0xd800 && n <= 0xdfff); }
static size_t rx_st_regex(const unsigned char *p, size_t len) {
    size_t code, classes, ranges, repeats, class_at, range_at, repeat_at, size, next;
    uint32_t captures, slots;
    if (len < 48) return len;
    if (len > 262144) return 40;
    if (!p || rx_st_word(p,0) != 0x58524e52) return 0;
    if (rx_st_word(p,4) != 1) return 4;
    if (rx_st_word(p,8) != 0x00100000) return 8;
    if (rx_st_word(p,44) != 0) return 44;
    code=rx_st_word(p,20); classes=rx_st_word(p,24); ranges=rx_st_word(p,28); repeats=rx_st_word(p,32);
    if (!code || code>8192) return 20;
    if (classes>256) return 24;
    if (ranges>8192) return 28;
    if (repeats>256) return 32;
    class_at=48+code*16; range_at=class_at+classes*8; repeat_at=range_at+ranges*8; size=repeat_at+repeats*16;
    if (size>262144 || size!=len || rx_st_word(p,40)!=size) return 40;
    if (rx_st_word(p,12)&~127u) return 12;
    captures=rx_st_word(p,16);
    if (captures>64) return 16;
    if (rx_st_word(p,36)>=code) return 36;
    slots=2*(captures+1);
    for (size_t i=0; i<code; i++) {
        size_t at=48+i*16;
        uint32_t op=rx_st_word(p,at), x=rx_st_word(p,at+4), y=rx_st_word(p,at+8), z=rx_st_word(p,at+12);
        int valid;
        switch (op) {
        case 0: case 2:
            if (x|y|z) return at+4;
            break;
        case 1: case 3: case 4: case 5: case 9: case 10:
            if (op==1) valid=rx_st_scalar(x);
            else if (op==3) valid=x<classes;
            else if (op==4) valid=x>=1 && x<=4;
            else if (op==5) valid=x>=2 && x<slots;
            else if (op==9) valid=x>=1 && x<=captures;
            else valid=x<repeats;
            if (!valid) return at+4;
            if (y|z) return at+8;
            break;
        case 6:
            if (x<2 || (x&1) || !y || (y&1) || (uint64_t)x+y>slots) return at+4;
            if (z) return at+12;
            break;
        case 14: case 15:
            if (op==14 ? (x<1 || x>4) : (x<1 || x>captures)) return at+4;
            if (y & ~(op==14 ? 6u : 2u)) return at+8;
            if (z) return at+12;
            break;
        case 7: case 8: case 11: case 12: case 13:
            if ((op==11 || op==12) ? x>=repeats : x>=code) return at+4;
            if (op==8) {
                if (y|z) return at+8;
            } else {
                if (y>=code) return at+8;
                if (op==11) { if (z>=code) return at+12; }
                else if ((op==13 && (z&~3u)) || (op!=13 && z)) return at+12;
            }
            break;
        default: return at;
        }
        if (((op>=1 && op<=6) || op==9 || op==10 || op==14 || op==15) && i+1==code) return at;
    }
    next=0;
    for (size_t i=0; i<classes; i++) {
        size_t at=class_at+i*8, count=rx_st_word(p,at+4);
        uint32_t previous=0;
        if (rx_st_word(p,at)!=next || count>ranges-next) return at;
        for (size_t j=0; j<count; j++) {
            size_t r=range_at+(next+j)*8;
            uint32_t lo=rx_st_word(p,r), hi=rx_st_word(p,r+4);
            if (!rx_st_scalar(lo) || !rx_st_scalar(hi) || lo>hi || (lo<0xd800 && hi>0xdfff)) return r;
            if (j && previous+1>=lo) return r;
            previous=hi;
        }
        next+=count;
    }
    if (next!=ranges) return range_at+next*8;
    for (size_t i=0; i<repeats; i++) {
        size_t at=repeat_at+i*16;
        uint32_t min=rx_st_word(p,at), max=rx_st_word(p,at+4);
        if (min>0x7fffffff) return at;
        if (max!=0xffffffff && (max>0x7fffffff || max<min)) return at+4;
        if (rx_st_word(p,at+8)&~1u) return at+8;
        if (rx_st_word(p,at+12)) return at+12;
    }
    return SIZE_MAX;
}
static size_t rx_st_utf8_error(const unsigned char *s, size_t n) {
    size_t at=0;
    while (at<n) {
        size_t start=at, more;
        unsigned char c=s[at++];
        if (c<0x80) continue;
        if (c>=0xc2 && c<=0xdf) more=1;
        else if (c>=0xe0 && c<=0xef) more=2;
        else if (c>=0xf0 && c<=0xf4) more=3;
        else return start;
        if (more>n-at) return start;
        if ((c==0xe0 && s[at]<0xa0) || (c==0xed && s[at]>=0xa0) ||
            (c==0xf0 && s[at]<0x90) || (c==0xf4 && s[at]>=0x90)) return start;
        for (size_t j=0; j<more; j++) if ((s[at++]&0xc0)!=0x80) return start;
    }
    return SIZE_MAX;
}
static size_t rx_st_span(const unsigned char *p, size_t at, int used,
                         const unsigned char *text, size_t n, size_t *cursor) {
    size_t offset=rx_st_word(p,at), len=rx_st_word(p,at+4);
    if (!used) return (offset|len) ? at : SIZE_MAX;
    if (offset!=*cursor) return at;
    if (len>n-*cursor) return at+4;
    if (*cursor!=n && (text[*cursor]&0xc0)==0x80) return at;
    if (*cursor+len!=n && (text[*cursor+len]&0xc0)==0x80) return at+4;
    *cursor+=len;
    return SIZE_MAX;
}
static size_t rx_st_replacement(const unsigned char *p, size_t len) {
    size_t count, blob, cursor=0, error;
    uint64_t size;
    if (len<32) return len;
    if (len>(4u<<20)) return 20;
    if (!p || rx_st_word(p,0)!=0x46524e52) return 0;
    if (rx_st_word(p,4)!=1) return 4;
    if (rx_st_word(p,8)!=0x00100000) return 8;
    if (rx_st_word(p,24)) return 24;
    if (rx_st_word(p,28)) return 28;
    count=rx_st_word(p,12);
    if (count>8192) return 12;
    blob=32+count*24;
    size=(uint64_t)blob+rx_st_word(p,16);
    if (size>(4u<<20) || size!=len || rx_st_word(p,20)!=size) return 20;
    error=rx_st_utf8_error(p+blob,len-blob);
    if (error!=SIZE_MAX) return blob+error;
    for (size_t i=0; i<count; i++) {
        size_t at=32+i*24;
        uint32_t op=rx_st_word(p,at), index=rx_st_word(p,at+4);
        if (op>11) return at;
        if (index>0x7fffffff || (!op && index)) return at+4;
        error=rx_st_span(p,at+8,!op || op>=9,p+blob,len-blob,&cursor);
        if (error!=SIZE_MAX) return error;
        error=rx_st_span(p,at+16,op==11,p+blob,len-blob,&cursor);
        if (error!=SIZE_MAX) return error;
    }
    return cursor!=len-blob ? blob+cursor : SIZE_MAX;
}
static size_t rx_st_payload(const unsigned char *p, size_t len) {
    size_t count, cursor;
    uint32_t previous=0;
    if (len<32) return len;
    if (len>(4u<<20)) return 16;
    if (!p || rx_st_word(p,0)!=0x58544e53) return 0;
    if (rx_st_word(p,4)!=1) return 4;
    if (rx_st_word(p,8)!=0x00100000) return 8;
    if (rx_st_word(p,20)) return 20;
    if (rx_st_word(p,24)) return 24;
    if (rx_st_word(p,28)) return 28;
    count=rx_st_word(p,12);
    if (count>512) return 12;
    cursor=32+count*24;
    if (rx_st_word(p,16)!=len || cursor>len) return 16;
    for (size_t i=0; i<count; i++) {
        size_t at=32+i*24, rlen, roff, flen, foff, error;
        uint32_t field=rx_st_word(p,at);
        if (field<=previous || field>512) return at;
        if (rx_st_word(p,at+20)) return at+20;
        if (rx_st_word(p,at+4)!=cursor) return at+4;
        rlen=rx_st_word(p,at+8);
        if (rlen>len-cursor) return at+8;
        roff=cursor; cursor+=rlen;
        if (rx_st_word(p,at+12)!=cursor) return at+12;
        flen=rx_st_word(p,at+16);
        if (flen>len-cursor) return at+16;
        foff=cursor; cursor+=flen;
        error=rx_st_regex(p+roff,rlen);
        if (error!=SIZE_MAX) return roff+error;
        error=rx_st_replacement(p+foff,flen);
        if (error!=SIZE_MAX) return foff+error;
        previous=field;
    }
    return cursor!=len ? 16 : SIZE_MAX;
}
int rx_snippet_transform_validate(const void *bytes, size_t len, size_t *error_offset) {
    size_t error=rx_st_payload((const unsigned char *)bytes,len);
    if (error_offset) *error_offset=error==SIZE_MAX ? 0 : error;
    return error==SIZE_MAX;
}

int rx_snippet_transform_program_validate(const rx_snippet_transform_program *p, int *part, size_t *error_offset) {
    size_t error=0;
    int which=0;
    if (p) {
        if (p->regex_len>262144) { which=1; error=40; }
        else if (p->replacement_len>(4u<<20)) { which=2; error=20; }
        else if (p->replacement_len>(4u<<20)-56-p->regex_len) { error=0; }
        else {
            error=rx_st_regex((const unsigned char *)p->regex,p->regex_len);
            if (error!=SIZE_MAX) which=1;
            else {
                error=rx_st_replacement((const unsigned char *)p->replacement,p->replacement_len);
                if (error!=SIZE_MAX) which=2;
            }
        }
    }
    if (part) *part=which;
    if (error_offset) *error_offset=error==SIZE_MAX ? 0 : error;
    return error==SIZE_MAX;
}

static int rx_snippet_ancestor(const rx_snippet *s, size_t parent, size_t child) {
    for (size_t p = s->fields[child].parent; p; p = s->fields[p - 1].parent)
        if (p - 1 == parent) return 1;
    return 0;
}

static size_t rx_snippet_extra_size(const rx_snippet *s) {
    size_t n = 0;
    for (size_t i = 0; i < s->nfields; i++) {
        rx_snippet_field f = s->fields[i];
        n += (size_t)snprintf(NULL, 0, "field %lld %zu %zu %zu 0\n", (long long)f.group, f.start, f.end, f.parent);
    }
    return n;
}

static int rx_snippet_validate_variants(const rx_snippet *s, const unsigned char variants[512]) {
    int64_t groups[256];
    size_t primary[256], depths[512], group_index[512], n = 0;
    unsigned char edges[256][256] = {{0}};
    size_t indegree[256] = {0}, queue[256], nq = 0;
    int final_seen = 0;
    if (!s || s->len > (4u << 20) || s->nfields > 512 ||
        (s->len && !s->text) || (s->nfields && !s->fields) ||
        !rx_snippet_utf8((const unsigned char *)s->text, s->len)) return 0;
    for (size_t i = 0; i < s->nfields; i++) {
        rx_snippet_field f = s->fields[i];
        if (f.group < 0 || f.group > 2147483647 || f.start > f.end || f.end > s->len || f.parent > i ||
            (f.start < s->len && ((unsigned char)s->text[f.start] & 0xc0) == 0x80) ||
            (f.end < s->len && ((unsigned char)s->text[f.end] & 0xc0) == 0x80)) return 0;
        depths[i] = 1;
        if (f.parent) {
            rx_snippet_field p = s->fields[f.parent - 1];
            if (p.group == 0 || primary[group_index[f.parent - 1]] != f.parent - 1 ||
                p.group == f.group || f.start < p.start || f.end > p.end) return 0;
            depths[i] = depths[f.parent - 1] + 1;
        }
        if (depths[i] > 32) return 0;
        if (!f.group) {
            if (final_seen || (variants && variants[i])) return 0;
            final_seen = 1;
            group_index[i] = 0;
            continue;
        }
        size_t g;
        for (g = 0; g < n && groups[g] != f.group; g++) {}
        if (g == n) {
            if (n == 256 || (variants && variants[i])) return 0;
            groups[n] = f.group;
            primary[n++] = i;
        } else {
            rx_snippet_field p = s->fields[primary[g]];
            int unequal = p.end - p.start != f.end - f.start ||
                (f.end > f.start && memcmp(s->text + p.start, s->text + f.start, f.end - f.start));
            if (!!unequal != !!(variants && variants[i])) return 0;
        }
        group_index[i] = g;
    }
    for (size_t i = 0; i < s->nfields; i++) {
        rx_snippet_field a = s->fields[i];
        for (size_t j = 0; j < i; j++) {
            rx_snippet_field b = s->fields[j];
            if (a.group && b.group && a.start < b.end && b.start < a.end &&
                (a.group == b.group || (!rx_snippet_ancestor(s, i, j) && !rx_snippet_ancestor(s, j, i)))) return 0;
        }
        if (!a.group) continue;
        size_t from = group_index[i];
        for (size_t p = a.parent; p; p = s->fields[p - 1].parent) {
            size_t to = group_index[p - 1];
            if (!edges[from][to]) { edges[from][to] = 1; indegree[to]++; }
        }
    }
    for (size_t i = 0; i < n; i++) if (!indegree[i]) queue[nq++] = i;
    for (size_t i = 0; i < nq; i++) {
        for (size_t to = 0; to < n; to++) {
            if (edges[queue[i]][to] && --indegree[to] == 0) queue[nq++] = to;
        }
    }
    if (nq != n) return 0;
    size_t extra = rx_snippet_extra_size(s);
    size_t header = (size_t)snprintf(NULL, 0, "0 0 0 0 snippet 0 %zu 0 %zu\n", s->len, extra);
    return header + s->len + extra <= (4u << 20);
}

int rx_snippet_validate(const rx_snippet *s) { return rx_snippet_validate_variants(s, NULL); }

static void rx_snippet_extra(const rx_snippet *s, rx_buf *out) {
    for (size_t i = 0; i < s->nfields; i++) {
        rx_snippet_field f = s->fields[i];
        rx_buf_fmt(out, "field %lld %zu %zu %zu 0\n", (long long)f.group, f.start, f.end, f.parent);
    }
}

static size_t rx_snippet_choice_size(const rx_snippet_choice *c) {
    size_t n = 0;
    for (size_t i = 0; i < c->noptions; i++)
        n += c->options[i].len + (size_t)snprintf(NULL, 0, "%zu\n", c->options[i].len);
    return n;
}

/* Length and identity preflight before any program bytes are inspected. */
static int rx_snippet_transforms_size(const rx_snippet_program *p, size_t *raw, size_t order[512]) {
    *raw=0;
    if (p->ntransforms>512 || (p->ntransforms && !p->transforms)) return 0;
    for (size_t i=0;i<512;i++) order[i]=SIZE_MAX;
    if (!p->ntransforms) return 1;
    size_t n=32+24*p->ntransforms;
    for (size_t i=0;i<p->ntransforms;i++) {
        const rx_snippet_field_transform *t=&p->transforms[i];
        if (!t->field || t->field>512 || t->field>p->snippet->nfields ||
            p->snippet->fields[t->field-1].group<=0 || order[t->field-1]!=SIZE_MAX) return 0;
        order[t->field-1]=i;
        if (t->transform.regex_len>262144 || t->transform.replacement_len>(4u<<20) ||
            t->transform.regex_len>(4u<<20)-n) return 0;
        n+=t->transform.regex_len;
        if (t->transform.replacement_len>(4u<<20)-n) return 0;
        n+=t->transform.replacement_len;
    }
    *raw=n;
    return 1;
}
static size_t rx_snippet_transforms_extra_size(size_t raw) {
    if (!raw) return 0;
    size_t encoded=4*((raw+2)/3);
    return encoded+(size_t)snprintf(NULL,0,"transforms 0 %zu\n",encoded);
}
static void rx_st_put(unsigned char *p, size_t at, size_t value) {
    for (size_t i=0;i<4;i++) p[at+i]=(unsigned char)(value>>(8*i));
}
/* Called only after validation and whole-body size checks. */
static void rx_snippet_transforms_extra(const rx_snippet_program *p, rx_buf *out) {
    if (!p->ntransforms) return;
    size_t raw,order[512];
    if (!rx_snippet_transforms_size(p,&raw,order)) abort();
    unsigned char *bytes=(unsigned char *)calloc(raw,1);
    if (!bytes) abort();
    rx_st_put(bytes,0,0x58544e53);rx_st_put(bytes,4,1);rx_st_put(bytes,8,0x00100000);
    rx_st_put(bytes,12,p->ntransforms);rx_st_put(bytes,16,raw);
    size_t row=32,cursor=32+24*p->ntransforms;
    for (size_t i=0;i<512;i++) {
        if (order[i]==SIZE_MAX) continue;
        const rx_snippet_transform_program *t=&p->transforms[order[i]].transform;
        rx_st_put(bytes,row,i+1);rx_st_put(bytes,row+4,cursor);rx_st_put(bytes,row+8,t->regex_len);
        memcpy(bytes+cursor,t->regex,t->regex_len);cursor+=t->regex_len;
        rx_st_put(bytes,row+12,cursor);rx_st_put(bytes,row+16,t->replacement_len);
        memcpy(bytes+cursor,t->replacement,t->replacement_len);cursor+=t->replacement_len;row+=24;
    }
    static const char alphabet[]="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    rx_buf_fmt(out,"transforms 0 %zu\n",4*((raw+2)/3));
    for (size_t i=0;i<raw;i+=3) {
        unsigned int a=bytes[i],b=i+1<raw?bytes[i+1]:0,c=i+2<raw?bytes[i+2]:0;
        char quartet[4]={alphabet[a>>2],alphabet[((a&3)<<4)|(b>>4)],
            i+1<raw?alphabet[((b&15)<<2)|(c>>6)]:'=',i+2<raw?alphabet[c&63]:'='};
        rx_buf_add(out,quartet,4);
    }
    free(bytes);
}

/* Only called after validation; each intermediate sum is bounded by the frame cap. */
static size_t rx_snippet_program_extra_size(const rx_snippet_program *p) {
    size_t n = rx_snippet_extra_size(p->snippet);
    for (size_t i=0; i<p->ninitial_variants; i++)
        n += (size_t)snprintf(NULL, 0, "variant %zu 0\n", p->initial_variants[i]);
    for (size_t i = 0; i < p->nchoices; i++) {
        const rx_snippet_choice *c = &p->choices[i];
        size_t size = rx_snippet_choice_size(c);
        n += size + (size_t)snprintf(NULL, 0, "choice %lld %zu %zu\n", (long long)c->group, c->noptions, size);
    }
    size_t raw,order[512];
    if (!rx_snippet_transforms_size(p,&raw,order)) abort();
    return n+rx_snippet_transforms_extra_size(raw);
}

int rx_snippet_program_validate(const rx_snippet_program *p) {
    if (!p || !p->snippet || p->ninitial_variants > 512 ||
        (p->ninitial_variants && !p->initial_variants) || p->nchoices > 256 || (p->nchoices && !p->choices)) return 0;
    unsigned char variants[512] = {0};
    for (size_t i=0; i<p->ninitial_variants; i++) {
        size_t index = p->initial_variants[i];
        if (!index || index > 512 || index > p->snippet->nfields || variants[index-1]) return 0;
        variants[index-1] = 1;
    }
    if (!rx_snippet_validate_variants(p->snippet, variants)) return 0;
    const rx_snippet *s = p->snippet;
    size_t raw,order[512];
    if (!rx_snippet_transforms_size(p,&raw,order)) return 0;
    size_t total = rx_snippet_extra_size(s);
    for (size_t i=0; i<p->ninitial_variants; i++)
        total += (size_t)snprintf(NULL, 0, "variant %zu 0\n", p->initial_variants[i]);
    for (size_t i = 0; i < p->nchoices; i++) {
        const rx_snippet_choice *c = &p->choices[i];
        if (c->group <= 0 || !c->noptions || c->noptions > 256 || !c->options) return 0;
        for (size_t j = 0; j < i; j++) if (p->choices[j].group == c->group) return 0;
        size_t primary;
        for (primary = 0; primary < s->nfields && s->fields[primary].group != c->group; primary++) {}
        if (primary == s->nfields) return 0;
        rx_snippet_field f = s->fields[primary];
        if (c->options[0].len != f.end - f.start) return 0;
        size_t size = 0;
        for (size_t j = 0; j < c->noptions; j++) {
            rx_snippet_option o = c->options[j];
            if (o.len > (1u << 20) || (o.len && !o.text) || !rx_snippet_utf8((const unsigned char *)o.text, o.len)) return 0;
            size += o.len + (size_t)snprintf(NULL, 0, "%zu\n", o.len);
            if (size > (4u << 20)) return 0;
        }
        if (f.end > f.start && memcmp(c->options[0].text, s->text + f.start, f.end - f.start)) return 0;
        total += size + (size_t)snprintf(NULL, 0, "choice %lld %zu %zu\n", (long long)c->group, c->noptions, size);
        if (total > (4u << 20)) return 0;
    }
    total+=rx_snippet_transforms_extra_size(raw);
    size_t header = (size_t)snprintf(NULL, 0, "0 0 0 0 snippet 0 %zu 0 %zu\n", s->len, total);
    if (header+s->len+total>(4u<<20)) return 0;
    for (size_t i=0;i<p->ntransforms;i++)
        if (!rx_snippet_transform_program_validate(&p->transforms[i].transform,NULL,NULL)) return 0;
    return 1;
}

static void rx_snippet_program_extra(const rx_snippet_program *p, rx_buf *out) {
    rx_snippet_extra(p->snippet, out);
    for (size_t i=0; i<p->ninitial_variants; i++)
        rx_buf_fmt(out, "variant %zu 0\n", p->initial_variants[i]);
    for (size_t i = 0; i < p->nchoices; i++) {
        const rx_snippet_choice *c = &p->choices[i];
        rx_buf_fmt(out, "choice %lld %zu %zu\n", (long long)c->group, c->noptions, rx_snippet_choice_size(c));
        for (size_t j = 0; j < c->noptions; j++) {
            rx_buf_fmt(out, "%zu\n", c->options[j].len);
            rx_buf_add(out, c->options[j].text, c->options[j].len);
        }
    }
    rx_snippet_transforms_extra(p,out);
}

int rx_snippet_program_encode(const rx_snippet_program *p, rx_buf *out) {
    if (!out || !rx_snippet_program_validate(p)) return -1;
    rx_buf_fmt(out, "0 0 0 0 snippet 0 %zu 0 %zu\n", p->snippet->len, rx_snippet_program_extra_size(p));
    rx_buf_add(out, p->snippet->text, p->snippet->len);
    rx_snippet_program_extra(p, out);
    return 0;
}

int rx_snippet_encode(const rx_snippet *s, rx_buf *out) {
    rx_snippet_program p = {s, NULL, 0, NULL, 0, NULL, 0};
    return rx_snippet_program_encode(&p, out);
}

int64_t rx_insert_snippet(rx_ext *ext, rx_doc doc, int64_t version, rx_range r, const rx_snippet *s) {
    rx_snippet_program p = {s, NULL, 0, NULL, 0, NULL, 0};
    return rx_insert_snippet_program(ext, doc, version, r, &p);
}

int64_t rx_insert_snippet_program(rx_ext *ext, rx_doc doc, int64_t version, rx_range r, const rx_snippet_program *p) {
    rx_buf body = {NULL, 0, 0}, fields = {NULL, 0, 0};
    int64_t result = -1;
    if (!rx_has(ext, "snippets") || (p && p->ninitial_variants && !rx_has(ext, "snippet-variants")) ||
        (p && p->ntransforms && !rx_has(ext,"snippet-transforms"))) {
        snprintf(ext->error, sizeof ext->error, "unknown snippets feature was not agreed");
        return -1;
    }
    if (doc <= 0 || version < 0 || r.start.line <= 0 || r.start.col <= 0 || r.end.line <= 0 || r.end.col <= 0 ||
        r.end.line < r.start.line || (r.end.line == r.start.line && r.end.col < r.start.col) ||
        rx_snippet_program_encode(p, &body) < 0) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid snippet target, range or fields");
        return -1;
    }
    char line[192];
    snprintf(line, sizeof line, "snippet %lld %lld %d %d %d %d %zu", (long long)doc, (long long)version,
             r.start.line, r.start.col, r.end.line, r.end.col, body.len);
    if (rx_request(ext, line, body.data, body.len, &fields, NULL) == 0 &&
        (!rx_positive_num(fields.data, INT64_MAX, &result) || result < version)) {
        snprintf(ext->error, sizeof ext->error, "syntax invalid snippet reply");
        result = -1;
    }
    rx_buf_free(&body);
    rx_buf_free(&fields);
    return result;
}

void rx_snippet_snapshot_free(rx_snippet_snapshot *s) {
    if (!s) return;
    rx_buf_free(&s->selected_text); rx_buf_free(&s->current_line); rx_buf_free(&s->path);
    rx_buf_free(&s->workspace); rx_buf_free(&s->indent); rx_buf_free(&s->line_comment);
    memset(s, 0, sizeof *s);
}

static int rx_snapshot_numbers(char *s, int64_t *out, size_t count) {
    for (size_t i=0; i<count; i++) {
        char *next = strchr(s, ' ');
        if ((i+1<count) != (next!=NULL)) return 0;
        if (next) *next = 0;
        if (!rx_positive_num(s, INT64_MAX, &out[i])) return 0;
        if (next) s = next+1;
    }
    return 1;
}

static int rx_snapshot_decode(const rx_buf *fields, const rx_buf *body, int64_t version, rx_pos at,
                              rx_snippet_snapshot *out) {
    char reply[128], head[257];
    int64_t r[2], n[11];
    size_t end=0, off, lengths[6];
    const char *parts[6];
    memset(out, 0, sizeof *out);
    if (!fields->data || fields->len>=sizeof reply || body->len>(4u<<20)) return 0;
    memcpy(reply, fields->data, fields->len); reply[fields->len]=0;
    if (!rx_snapshot_numbers(reply,r,2) || r[0]!=version || r[1]!=(int64_t)body->len) return 0;
    while (end<body->len && end<=256 && body->data[end]!='\n') end++;
    if (end>=body->len || end>256 || memchr(body->data,0,end)) return 0;
    memcpy(head,body->data,end); head[end]=0;
    if (strncmp(head,"context 1 ",10) || !rx_snapshot_numbers(head+10,n,11) || at.line<=0 || at.col<=0 ||
        n[0]!=at.line || n[1]!=at.col || n[2]<1 || n[2]>16 || n[3]>1 || n[4]>1) return 0;
    off=end+1;
    for (size_t i=0; i<6; i++) {
        if ((uint64_t)n[i+5]>body->len-off) return 0;
        lengths[i]=(size_t)n[i+5]; parts[i]=body->data+off;
        if (!rx_snippet_utf8((const unsigned char *)parts[i],lengths[i])) return 0;
        off+=lengths[i];
    }
    size_t col=(size_t)at.col-1, indent=0;
    if (off!=body->len || memchr(parts[1],'\n',lengths[1]) || col>lengths[1] ||
        (col<lengths[1] && ((unsigned char)parts[1][col]&0xc0)==0x80)) return 0;
    while (indent<col && (parts[1][indent]==' ' || parts[1][indent]=='\t')) indent++;
    if (lengths[4]!=indent || (indent && memcmp(parts[4],parts[1],indent))) return 0;
    out->version=version; out->position=at; out->tab_width=(int)n[2];
    out->insert_spaces=(int)n[3]; out->crlf=(int)n[4];
    rx_buf *buffers[6]={&out->selected_text,&out->current_line,&out->path,&out->workspace,&out->indent,&out->line_comment};
    for (size_t i=0; i<6; i++) rx_buf_add(buffers[i],parts[i],lengths[i]);
    return 1;
}

int rx_get_snippet_snapshot(rx_ext *ext, rx_doc doc, int64_t version, rx_range r, rx_snippet_snapshot *out) {
    if (!out) { snprintf(ext->error,sizeof ext->error,"syntax missing snippet context output"); return -1; }
    memset(out,0,sizeof *out);
    if (!rx_has(ext,"snippets")) {
        snprintf(ext->error,sizeof ext->error,"unknown snippets feature was not agreed"); return -1;
    }
    if (doc<=0 || version<0 || r.start.line<=0 || r.start.col<=0 || r.end.line<=0 || r.end.col<=0 ||
        r.end.line<r.start.line || (r.end.line==r.start.line && r.end.col<r.start.col)) {
        snprintf(ext->error,sizeof ext->error,"syntax invalid snippet target or range"); return -1;
    }
    char line[192]; rx_buf fields={NULL,0,0}, body={NULL,0,0};
    snprintf(line,sizeof line,"snippet-context %lld %lld %d %d %d %d",(long long)doc,(long long)version,
        r.start.line,r.start.col,r.end.line,r.end.col);
    int result=rx_request(ext,line,NULL,0,&fields,&body);
    if (result==0 && !rx_snapshot_decode(&fields,&body,version,r.start,out)) {
        snprintf(ext->error,sizeof ext->error,"syntax invalid snippet context reply"); result=-1;
    }
    rx_buf_free(&fields); rx_buf_free(&body);
    return result;
}

static int rx_source_kind(const char *s) {
    int segment = 0;
    if (!s || strlen(s) > 128) return 0;
    if (strcmp(s, "source") == 0) return 1;
    if (strncmp(s, "source.", 7) != 0) return 0;
    for (s += 7; *s; s++) {
        if (*s == '.') { if (!segment) return 0; segment = 0; }
        else if ((*s >= 'a' && *s <= 'z') || (*s >= 'A' && *s <= 'Z') ||
                 (*s >= '0' && *s <= '9') || *s == '_' || *s == '-') segment = 1;
        else return 0;
    }
    return segment;
}

/* ---- pictures */

/* a number as the protocol writes it, at most two decimals, whatever the C locale says */
static void rx_num(rx_buf *b, double v) {
    if (!(v == v)) v = 0;                       /* NaN */
    if (v > 9999999) v = 9999999;
    if (v < -9999999) v = -9999999;
    long long cents = (long long)(v * 100 + (v < 0 ? -0.5 : 0.5));
    char s[48];
    int neg = cents < 0;
    if (neg) cents = -cents;
    long long whole = cents / 100, frac = cents % 100;
    if (frac == 0) snprintf(s, sizeof s, "%s%lld", neg && whole ? "-" : "", whole);
    else if (frac % 10 == 0) snprintf(s, sizeof s, "%s%lld.%lld", neg ? "-" : "", whole, frac / 10);
    else snprintf(s, sizeof s, "%s%lld.%02lld", neg ? "-" : "", whole, frac);
    rx_buf_add(b, " ", 1);
    rx_buf_str(b, s);
}

/* a decimal number as rhun writes it, whatever the C locale says */
static double rx_atof(const char *s) {
    double v = 0, scale = 1;
    int neg = *s == '-';
    if (neg) s++;
    for (; *s >= '0' && *s <= '9'; s++) v = v * 10 + (*s - '0');
    if (*s == '.')
        for (s++; *s >= '0' && *s <= '9'; s++) scale /= 10, v += (*s - '0') * scale;
    return neg ? -v : v;
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
    rx_list_item_cursor(l, r, kind, label, insert, detail, doc, edits, texts, nedits, -1);
}

void rx_list_item_cursor(rx_list *l, rx_range r, const char *kind, const char *label, const char *insert,
                         const char *detail, const char *doc, const rx_range *edits, const char *const *texts,
                         int nedits, long cursor) {
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
    if (cursor >= 0 && (size_t)cursor <= il && ((size_t)cursor == il || ((unsigned char)insert[cursor] & 0xc0) != 0x80))
        rx_buf_fmt(&extra, "cursor %ld 0\n", cursor);
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

void rx_list_item_snippet(rx_list *l, rx_range r, const char *kind, const char *label,
                         const rx_snippet *s, const char *detail, const char *doc,
                         const rx_range *edits, const char *const *texts, int nedits) {
    rx_snippet_program program = {s, NULL, 0, NULL, 0, NULL, 0};
    rx_list_item_snippet_program(l, r, kind, label, &program, detail, doc, edits, texts, nedits);
}

void rx_list_item_snippet_program(rx_list *l, rx_range r, const char *kind, const char *label,
                         const rx_snippet_program *p, const char *detail, const char *doc,
                         const rx_range *edits, const char *const *texts, int nedits) {
    rx_buf extra = {NULL, 0, 0};
    const rx_snippet *s = p ? p->snippet : NULL;
    if (!s || s->len > (1u << 20) || (s->len && !s->text)) { l->more = 1; return; }
    for (int i = 0; i < nedits; i++) {
        size_t n = strlen(texts[i]);
        rx_buf_fmt(&extra, "edit %d %d %d %d %zu\n", edits[i].start.line, edits[i].start.col,
                   edits[i].end.line, edits[i].end.col, n);
        rx_buf_add(&extra, texts[i], n);
    }
    if (doc && *doc) {
        rx_buf_fmt(&extra, "doc %zu\n", strlen(doc));
        rx_buf_str(&extra, doc);
    }
    if (rx_snippet_program_validate(p)) rx_snippet_program_extra(p, &extra);
    if (!label) label = "";
    if (!detail) detail = "";
    size_t ll = rx_one_line_len(label), dl = rx_one_line_len(detail);
    if (l->n >= 4096 || ll > (1u << 20) || dl > (1u << 20) || extra.len > (1u << 20) ||
        l->items.len + 64 + ll + s->len + dl + extra.len > (1u << 20) - 64) {
        l->more = 1;
        rx_buf_free(&extra);
        return;
    }
    rx_buf_fmt(&l->items, "%d %d %d %d %s %zu %zu %zu %zu\n", r.start.line, r.start.col, r.end.line, r.end.col,
               kind && *kind ? kind : "text", ll, s->len, dl, extra.len);
    rx_buf_add(&l->items, label, ll);
    rx_buf_add(&l->items, s->text, s->len);
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
    char head[32];
    int hl = snprintf(head, sizeof head, "%d %d\n", from, to);
    if ((size_t)hl + sl + 1 > 65536) return;   /* a signature over 64 KB: no answer hides the help */
    rx_buf_add(out, head, (size_t)hl);
    rx_buf_add(out, signature, sl);
    rx_buf_add(out, "\n", 1);
    if (!doc) return;
    size_t room = 65536 - ((size_t)hl + sl + 1), dl = strlen(doc);
    if (dl > room) {
        dl = room;
        while (dl > 0 && ((unsigned char)doc[dl] & 0xc0) == 0x80) dl--;   /* not inside a character */
    }
    rx_buf_add(out, doc, dl);
}

/* ---- definition, references, symbols, code actions, rename and format */

#define RX_RECORDS 10000                        /* locations, symbols (12d, 12e) */
#define RX_RECORDS_BODY ((size_t)1 << 20)
#define RX_ACTIONS 1000                         /* code actions (12f), their edits included */
#define RX_ACTIONS_BODY ((size_t)4 << 20)
#define RX_WEDIT_FILES 1000                     /* a workspace edit (12g) */
#define RX_WEDIT_EDITS 100000
#define RX_WEDIT_BODY ((size_t)4 << 20)
#define RX_TEXT_MAX 1000                        /* a name, title, detail or preview (12d) */
#define RX_KIND_MAX 40                          /* a kind word */
#define RX_PATH_MAX 4096

/* how many of the n bytes of s fit in max, cut at the start of a character */
static size_t rx_cut(const char *s, size_t n, size_t max) {
    if (n <= max) return n;
    n = max;
    while (n > 0 && ((unsigned char)s[n] & 0xc0) == 0x80) n--;
    return n;
}

/* a line or column as rhun reads it: not negative */
static int rx_nn(int v) { return v < 0 ? 0 : v; }

static void rx_buf_clear(rx_buf *b) {
    b->len = 0;
    if (b->data) b->data[0] = 0;
}

/* n bytes of s with its line breaks as spaces: a one-line field */
static void rx_add_line(rx_buf *b, const char *s, size_t n) {
    size_t at = b->len;
    rx_buf_add(b, s, n);
    for (size_t i = at; i < b->len; i++)
        if (b->data[i] == '\n' || b->data[i] == '\r') b->data[i] = ' ';
}

/* a one-word field: s without spaces and control characters, at most 40 bytes, or dflt when nothing
 * is left */
static void rx_add_word(rx_buf *b, const char *s, const char *dflt) {
    size_t at = b->len;
    for (; s && *s; s++)
        if ((unsigned char)*s > ' ' && *s != 0x7f) rx_buf_add(b, s, 1);
    if (b->len == at) rx_buf_str(b, dflt);
    b->len = at + rx_cut(b->data + at, b->len - at, RX_KIND_MAX);
    b->data[b->len] = 0;
}

/* PATH (12d): the path, or #DOC */
static const char *rx_target(const char *path, rx_doc doc, char *tmp, size_t cap) {
    if (path && *path) return path;
    snprintf(tmp, cap, "#%lld", (long long)doc);
    return tmp;
}

/* keeps the record added after mark when the body still fits in max with its "N" line; else
 * drops it, and every later one (what is kept stays in order, with nothing missing between) */
static void rx_keep(rx_buf *recs, size_t mark, int *n, int *full, size_t max) {
    if (recs->len + 16 > max) {
        recs->len = mark;
        recs->data[mark] = 0;
        *full = 1;
        return;
    }
    (*n)++;
}

static void rx_records_finish(rx_buf *recs, int *n, int *full, rx_buf *out) {
    rx_buf_fmt(out, "%d\n", *n);
    rx_buf_add(out, recs->data, recs->len);
    rx_buf_free(recs);
    *n = *full = 0;
}

static int rx_blank(char c) { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'; }

void rx_location(rx_locations *l, const char *path, rx_doc doc, rx_range r, const char *preview) {
    if (l->n >= RX_RECORDS) l->full = 1;
    if (l->full) return;
    char tmp[32];
    const char *t = rx_target(path, doc, tmp, sizeof tmp), *p = preview ? preview : "";
    size_t tl = strlen(t), pl = strlen(p), mark = l->recs.len;
    if (tl > RX_PATH_MAX) return;
    while (pl && rx_blank(p[0])) p++, pl--;
    while (pl && rx_blank(p[pl - 1])) pl--;
    pl = rx_cut(p, pl, RX_TEXT_MAX);
    rx_buf_fmt(&l->recs, "%d %d %d %d %zu %zu\n", rx_nn(r.start.line), rx_nn(r.start.col), rx_nn(r.end.line),
               rx_nn(r.end.col), tl, pl);
    rx_buf_add(&l->recs, t, tl);
    rx_add_line(&l->recs, p, pl);
    rx_keep(&l->recs, mark, &l->n, &l->full, RX_RECORDS_BODY);
}

void rx_symbol(rx_symbols *l, rx_range r, const char *kind, int depth, const char *name, const char *detail) {
    if (l->n >= RX_RECORDS) l->full = 1;
    if (l->full) return;
    if (!name) name = "";
    if (!detail) detail = "";
    size_t nl = rx_cut(name, strlen(name), RX_TEXT_MAX), dl = rx_cut(detail, strlen(detail), RX_TEXT_MAX);
    size_t mark = l->recs.len;
    if (!nl) return;
    rx_buf_fmt(&l->recs, "%d %d %d %d ", rx_nn(r.start.line), rx_nn(r.start.col), rx_nn(r.end.line), rx_nn(r.end.col));
    rx_add_word(&l->recs, kind, "function");
    rx_buf_fmt(&l->recs, " %d %zu %zu\n", depth < 0 ? 0 : depth, nl, dl);
    rx_add_line(&l->recs, name, nl);
    rx_add_line(&l->recs, detail, dl);
    rx_keep(&l->recs, mark, &l->n, &l->full, RX_RECORDS_BODY);
}

void rx_locations_finish(rx_locations *l, rx_buf *out) { rx_records_finish(&l->recs, &l->n, &l->full, out); }
void rx_symbols_finish(rx_symbols *l, rx_buf *out) { rx_records_finish(&l->recs, &l->n, &l->full, out); }
void rx_actions_finish(rx_actions *l, rx_buf *out) { rx_records_finish(&l->recs, &l->n, &l->full, out); }

/* frees the edits; what failed stays */
static void rx_wedit_drop(rx_wedit *e) {
    rx_buf_free(&e->body);
    rx_buf_free(&e->group);
    rx_buf_free(&e->target);
    e->files = e->edits = e->group_edits = e->open = 0;
}

void rx_wedit_free(rx_wedit *e) {
    rx_wedit_drop(e);
    e->version = 0;
    e->fail = NULL;
    e->why[0] = 0;
}

/* the first failure wins: the edit is dropped, and every later call does nothing */
static void rx_wedit_fail(rx_wedit *e, const char *code, const char *why) {
    if (e->fail) return;
    rx_wedit_drop(e);
    size_t n = strlen(why);
    if (n > sizeof e->why - 1) {
        n = sizeof e->why - 1;
        while (n > 0 && ((unsigned char)why[n] & 0xc0) == 0x80) n--;   /* not inside a character */
    }
    memmove(e->why, why, n);
    e->why[n] = 0;
    for (char *c = e->why; *c; c++)
        if (*c == '\n' || *c == '\r') *c = ' ';
    e->fail = code;
}

int rx_refuse(rx_wedit *e, const char *why) {
    e->fail = NULL;                         /* the handler's reason wins over a limit it ran into */
    rx_wedit_fail(e, "refused", why ? why : "");
    return -1;
}

/* the open file's header and edits go after the files before it */
static void rx_wedit_flush(rx_wedit *e) {
    if (!e->open) return;
    char v[32];
    if (e->version < 0) snprintf(v, sizeof v, "-");
    else snprintf(v, sizeof v, "%lld", (long long)e->version);
    rx_buf_fmt(&e->body, "%zu %s %d\n", e->target.len, v, e->group_edits);
    rx_buf_add(&e->body, e->target.data, e->target.len);
    rx_buf_add(&e->body, e->group.data, e->group.len);
    rx_buf_clear(&e->target);
    rx_buf_clear(&e->group);
    e->group_edits = e->open = 0;
}

/* what the body holds already is past 4 MB: its headers would only add to it */
static void rx_wedit_check(rx_wedit *e) {
    if (e->body.len + e->target.len + e->group.len > RX_WEDIT_BODY) rx_wedit_fail(e, "toobig", "over 4194304 bytes");
}

void rx_wedit_file(rx_wedit *e, const char *path, rx_doc doc, int64_t version) {
    if (e->fail) return;
    rx_wedit_flush(e);
    if (e->files >= RX_WEDIT_FILES) {
        rx_wedit_fail(e, "toobig", "more than 1000 files");
        return;
    }
    char tmp[32];
    const char *t = rx_target(path, doc, tmp, sizeof tmp);
    if (strlen(t) > RX_PATH_MAX) {
        rx_wedit_fail(e, "toobig", "a path over 4096 bytes");
        return;
    }
    rx_buf_str(&e->target, t);
    e->version = version < 0 ? -1 : version;
    e->files++;
    e->open = 1;
    rx_wedit_check(e);
}

void rx_wedit_edit(rx_wedit *e, rx_range r, const char *text, size_t len) {
    if (e->fail) return;
    if (!e->open) {
        rx_wedit_fail(e, "syntax", "an edit before its file (rx_wedit_file)");
        return;
    }
    if (e->edits >= RX_WEDIT_EDITS) {
        rx_wedit_fail(e, "toobig", "more than 100000 edits");
        return;
    }
    if (len > RX_WEDIT_BODY) {
        rx_wedit_fail(e, "toobig", "over 4194304 bytes");
        return;
    }
    rx_buf_fmt(&e->group, "%d %d %d %d %zu\n", rx_nn(r.start.line), rx_nn(r.start.col), rx_nn(r.end.line),
               rx_nn(r.end.col), len);
    rx_buf_add(&e->group, text, len);
    e->edits++;
    e->group_edits++;
    rx_wedit_check(e);
}

int rx_wedit_finish(rx_wedit *e, rx_buf *out) {
    if (!e->fail) {
        rx_wedit_flush(e);
        char head[32];
        int hl = snprintf(head, sizeof head, "%d\n", e->files);
        if ((size_t)hl + e->body.len > RX_WEDIT_BODY) {
            rx_wedit_fail(e, "toobig", "over 4194304 bytes");
        } else {
            rx_buf_add(out, head, (size_t)hl);
            rx_buf_add(out, e->body.data, e->body.len);
        }
    }
    rx_wedit_drop(e);
    return e->fail ? -1 : 0;
}

void rx_action(rx_actions *l, const char *kind, const char *title, rx_wedit *edit, const void *data, size_t len) {
    rx_buf eb = {NULL, 0, 0};
    int bad = edit && rx_wedit_finish(edit, &eb) < 0;
    if (edit) rx_wedit_free(edit);
    if (!title) title = "";
    size_t tl = rx_cut(title, strlen(title), RX_TEXT_MAX), mark = l->recs.len;
    if (!tl) bad = 1;                       /* no title: nothing to list */
    if (!bad && l->n >= RX_ACTIONS) l->full = 1;
    if (bad || l->full) {
        rx_buf_free(&eb);
        return;
    }
    rx_add_word(&l->recs, kind, "quickfix");
    rx_buf_fmt(&l->recs, " %zu %zu %zu\n", tl, eb.len, len);
    rx_add_line(&l->recs, title, tl);
    rx_buf_add(&l->recs, eb.data, eb.len);
    rx_buf_add(&l->recs, data, len);
    rx_buf_free(&eb);
    rx_keep(&l->recs, mark, &l->n, &l->full, RX_ACTIONS_BODY);
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

const char *rx_event_tail(const rx_event *ev, int n) {
    const char *p = ev->rest;
    for (int i = 0; i < n && p; i++) {
        p = strchr(p, ' ');
        if (p) p++;
    }
    return p ? p : "";
}

/* ---- the loop */

/* fields f[from] to f[to - 1] are numbers: digits only */
static int rx_nums(char **f, int from, int to) {
    for (int i = from; i < to; i++) {
        const char *s = f[i];
        if (!*s) return 0;
        for (; *s; s++)
            if (*s < '0' || *s > '9') return 0;
    }
    return 1;
}

static void rx_answer(rx_ext *ext, const rx_handlers *h, void *user, char **f, int n,
                      const unsigned char *rbody, size_t rbody_len) {
    /* ID VERB DOC VERSION, then LINE COL KIND CHAR (12b to 12e), L1 C1 L2 C2 (actions), LINE COL
     * invoked NAME (rename) or TAB_WIDTH SPACES (format) */
    const char *verb = f[1];
    int ok = 0;
    rx_buf body = {NULL, 0, 0}, line = {NULL, 0, 0};
    rx_wedit e;
    memset(&e, 0, sizeof e);
    int is_pos = strcmp(verb, "definition") == 0 || strcmp(verb, "references") == 0 || strcmp(verb, "symbols") == 0;
    if ((strcmp(verb, "complete") == 0 || strcmp(verb, "signature") == 0) && n >= 8) {
        rx_ask req;
        req.id = atoll(f[0]);
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.at.line = atoi(f[4]);
        req.at.col = atoi(f[5]);
        req.kind = f[6];
        req.ch = strcmp(f[6], "trigger") == 0 ? f[7] : "";
        if (strcmp(verb, "complete") == 0 && h->complete) ok = h->complete(ext, &req, &body, user);
        else if (strcmp(verb, "signature") == 0 && h->signature) ok = h->signature(ext, &req, &body, user);
    } else if (is_pos && n >= 6 && rx_nums(f, 2, 6)) {
        rx_ask req;
        req.id = atoll(f[0]);
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.at.line = atoi(f[4]);
        req.at.col = atoi(f[5]);
        req.kind = n > 6 ? f[6] : "invoked";
        req.ch = "";
        if (verb[0] == 's') {
            rx_symbols l;
            memset(&l, 0, sizeof l);
            if (h->symbols && (ok = h->symbols(ext, &req, &l, user)) != 0) rx_symbols_finish(&l, &body);
            rx_buf_free(&l.recs);
        } else {
            rx_locations l;
            memset(&l, 0, sizeof l);
            int (*fn)(rx_ext *, const rx_ask *, rx_locations *, void *) = verb[0] == 'd' ? h->definition : h->references;
            if (fn && (ok = fn(ext, &req, &l, user)) != 0) rx_locations_finish(&l, &body);
            rx_buf_free(&l.recs);
        }
    } else if (strcmp(verb, "actions") == 0 && n >= 8 && rx_nums(f, 2, 8) && h->actions) {
        rx_actions_ask req;
        req.id = atoll(f[0]);
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.range.start.line = atoi(f[4]);
        req.range.start.col = atoi(f[5]);
        req.range.end.line = atoi(f[6]);
        req.range.end.col = atoi(f[7]);
        rx_actions l;
        memset(&l, 0, sizeof l);
        if ((ok = h->actions(ext, &req, &l, user)) != 0) rx_actions_finish(&l, &body);
        rx_buf_free(&l.recs);
    } else if (strcmp(verb, "rename") == 0 && n >= 8 && rx_nums(f, 2, 6) && h->rename) {
        rx_rename_ask req;
        req.id = atoll(f[0]);
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.at.line = atoi(f[4]);
        req.at.col = atoi(f[5]);
        req.name = f[7];
        if ((ok = h->rename(ext, &req, &e, user)) != 0) rx_wedit_finish(&e, &body);
    } else if (strcmp(verb, "format") == 0 && n >= 6 && rx_nums(f, 2, 5) && h->format &&
               (strcmp(f[5], "yes") == 0 || strcmp(f[5], "no") == 0)) {
        rx_format_ask req;
        req.id = atoll(f[0]);
        req.doc = atoll(f[2]);
        req.version = atoll(f[3]);
        req.tab_width = atoi(f[4]);
        req.spaces = strcmp(f[5], "yes") == 0;
        if ((ok = h->format(ext, &req, &e, user)) != 0) rx_wedit_finish(&e, &body);
    } else if (strcmp(verb, "save-actions") == 0 && n == 5 && h->save_actions && rx_has(ext, "save-actions")) {
        rx_save_actions_ask req;
        if (rx_positive_num(f[0], 2147483647, &req.id) && req.id > 0 &&
            rx_positive_num(f[2], INT64_MAX, &req.doc) && req.doc > 0 &&
            rx_positive_num(f[3], INT64_MAX, &req.version) && rx_source_kind(f[4])) {
            req.kind = f[4];
            if ((ok = h->save_actions(ext, &req, &e, user)) != 0) rx_wedit_finish(&e, &body);
        }
    } else if (strcmp(verb, "quickdiff") == 0 && n == 4 && h->quickdiff && rx_has(ext, "quickdiff")) {
        rx_quickdiff_ask req;
        if (rx_positive_num(f[0], 2147483647, &req.id) && req.id > 0 &&
            rx_positive_num(f[2], INT64_MAX, &req.doc) && req.doc > 0 &&
            rx_positive_num(f[3], INT64_MAX, &req.version))
            ok = h->quickdiff(ext, &req, &body, user);
    } else if (strcmp(verb, "commit-message") == 0 && n == 3 && h->commit_message && rx_has(ext, "commit-message")) {
        rx_commit_message_ask req;
        if (rx_positive_num(f[0], 2147483647, &req.id) && req.id > 0 &&
            rx_parse_commit_message(req.id, rbody, rbody_len, &req) == 0) {
            rx_buf msg = {NULL, 0, 0};
            int r = h->commit_message(ext, &req, &msg, user);
            size_t start = 0, len = 0;
            if (r > 0 && rx_check_commit_message(msg.data, msg.len, &start, &len) == 0) {
                rx_buf_add(&body, msg.data + start, len);
                ok = 1;
            } else if (r != 0) {
                /* refused: the handler's reason, or rhun's check, on one line */
                e.fail = "refused";
                size_t w = 0;
                const char *why = r < 0 && msg.len ? msg.data : "an empty, oversized or invalid commit message";
                size_t wl = r < 0 && msg.len ? msg.len : strlen(why);
                for (size_t i = 0; i < wl && w + 1 < sizeof e.why; i++) e.why[w++] = why[i] == '\n' || why[i] == '\r' ? ' ' : why[i];
                e.why[w] = 0;
                ok = 1;
            }
            rx_buf_free(&msg);
        }
    }
    if (ok && e.fail) {
        rx_buf_fmt(&line, "error %s %s %s", f[0], e.fail, e.why);     /* why is one line */
        rx_write(line.data, NULL, 0);
    } else if (ok) {
        rx_buf_fmt(&line, "ok %s %zu", f[0], body.len);
        rx_write(line.data, body.data, body.len);
    } else {
        rx_buf_fmt(&line, "error %s unknown no such request", f[0]);
        rx_write(line.data, NULL, 0);
    }
    rx_wedit_free(&e);
    rx_buf_free(&line);
    rx_buf_free(&body);
}

/* ---- notebooks */

void rx_nb_begin(rx_buf *out, int cells, const void *meta, size_t meta_len) {
    rx_buf_fmt(out, "%d %zu\n", cells, meta_len);
    rx_buf_add(out, meta, meta_len);
}

void rx_nb_cell(rx_buf *out, const char *kind, const char *lang, int64_t count, const void *source,
                size_t source_len, const void *meta, size_t meta_len, int outputs) {
    int markup = strcmp(kind, "markup") == 0;
    rx_buf_fmt(out, "%s ", markup ? "markup" : "code");
    if (markup || !lang || !*lang) rx_buf_str(out, "-");
    else for (const char *p = lang; *p; p++) rx_buf_add(out, *p == ' ' ? "_" : p, 1);
    if (count >= 0) rx_buf_fmt(out, " %lld", (long long)count);
    else rx_buf_str(out, " -");
    rx_buf_fmt(out, " %zu %zu %d\n", source_len, meta_len, outputs);
    rx_buf_add(out, source, source_len);
    rx_buf_add(out, meta, meta_len);
}

static int rx_nb_token(const char *t, size_t n) {
    if (!n || n > 64) return 0;
    for (size_t i = 0; i < n; i++) {
        char c = t[i];
        if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strchr("_.+/-", c))) return 0;
    }
    return 1;
}

void rx_nb_output(rx_buf *out, const char *kind, const char *id, const void *keep, size_t keep_len, int items) {
    rx_buf_fmt(out, "%s %s %zu %d\n", kind, id && rx_nb_token(id, strlen(id)) ? id : "-", keep_len, items);
    rx_buf_add(out, keep, keep_len);
}

void rx_nb_item(rx_buf *out, const char *type, int w, int h, const void *data, size_t len) {
    rx_buf_fmt(out, "%s %d %d %zu\n", type && rx_nb_token(type, strlen(type)) ? type : "text", w, h, len);
    rx_buf_add(out, data, len);
}

/* rx_nb_fields: the next line of the reader as up to 8 fields copied into buf */
static int rx_nb_fields(rx_nb_reader *r, char *buf, size_t cap, char **f) {
    const unsigned char *nl = r->p;
    while (nl < r->end && *nl != '\n') nl++;
    if (nl >= r->end || (size_t)(nl - r->p) >= cap) return -1;
    memcpy(buf, r->p, (size_t)(nl - r->p));
    buf[nl - r->p] = 0;
    r->p = nl + 1;
    int n = 0;
    char *p = buf;
    while (*p && n < 8) {
        while (*p == ' ') p++;
        if (!*p) break;
        f[n++] = p;
        while (*p && *p != ' ') p++;
        if (*p) *p++ = 0;
    }
    return n;
}

static int rx_nb_size(const char *s, long long max, size_t *out) {
    if (!*s || strlen(s) > 18) return 0;
    long long v = 0;
    for (; *s; s++) {
        if (*s < '0' || *s > '9') return 0;
        v = v * 10 + (*s - '0');
    }
    if (v > max) return 0;
    *out = (size_t)v;
    return 1;
}

static const unsigned char *rx_nb_take(rx_nb_reader *r, size_t n) {
    if ((size_t)(r->end - r->p) < n) return NULL;
    const unsigned char *b = r->p;
    r->p += n;
    return b;
}

int rx_nb_read_begin(rx_nb_reader *r, const unsigned char *body, size_t len) {
    char buf[128], *f[8];
    size_t n, ml;
    r->p = body;
    r->end = body + len;
    if (rx_nb_fields(r, buf, sizeof buf, f) != 2 || !rx_nb_size(f[0], 5000, &n) ||
        !rx_nb_size(f[1], (long long)64 << 20, &ml) || !(r->meta = rx_nb_take(r, ml)))
        return 0;
    r->cells = (int)n;
    r->meta_len = ml;
    return 1;
}

int rx_nb_read_cell(rx_nb_reader *r, rx_nb_cell_rec *c) {
    char buf[256], *f[8];
    size_t sl, ml, no;
    if (rx_nb_fields(r, buf, sizeof buf, f) != 6 || (strcmp(f[0], "code") && strcmp(f[0], "markup")) ||
        strlen(f[1]) >= sizeof c->lang || !rx_nb_size(f[3], 4 << 20, &sl) ||
        !rx_nb_size(f[4], (long long)64 << 20, &ml) || !rx_nb_size(f[5], 1000, &no))
        return 0;
    snprintf(c->kind, sizeof c->kind, "%s", f[0]);
    snprintf(c->lang, sizeof c->lang, "%s", f[1]);
    c->count = strcmp(f[2], "-") == 0 ? -1 : atoll(f[2]);
    if (!(c->source = rx_nb_take(r, sl)) || !(c->meta = rx_nb_take(r, ml))) return 0;
    c->source_len = sl;
    c->meta_len = ml;
    c->outputs = (int)no;
    return 1;
}

int rx_nb_read_output(rx_nb_reader *r, rx_nb_output_rec *o) {
    char buf[256], *f[8];
    size_t kl, ni;
    if (rx_nb_fields(r, buf, sizeof buf, f) != 4 || strlen(f[0]) >= sizeof o->kind ||
        (strcmp(f[1], "-") && !rx_nb_token(f[1], strlen(f[1]))) ||
        !rx_nb_size(f[2], (long long)64 << 20, &kl) || !rx_nb_size(f[3], 32, &ni))
        return 0;
    snprintf(o->kind, sizeof o->kind, "%s", f[0]);
    snprintf(o->id, sizeof o->id, "%s", strcmp(f[1], "-") ? f[1] : "");
    if (!(o->keep = rx_nb_take(r, kl))) return 0;
    o->keep_len = kl;
    o->items = (int)ni;
    return 1;
}

int rx_nb_read_item(rx_nb_reader *r, rx_nb_item_rec *it) {
    char buf[256], *f[8];
    size_t w, h, len;
    if (rx_nb_fields(r, buf, sizeof buf, f) != 4 || !rx_nb_token(f[0], strlen(f[0])) ||
        !rx_nb_size(f[1], 65536, &w) || !rx_nb_size(f[2], 65536, &h) || !rx_nb_size(f[3], 16 << 20, &len))
        return 0;
    snprintf(it->type, sizeof it->type, "%s", f[0]);
    it->w = (int)w;
    it->h = (int)h;
    if (!(it->data = rx_nb_take(r, len))) return 0;
    it->len = len;
    return 1;
}

static int rx_nb_agreed(rx_ext *ext) {
    if (rx_has(ext, "notebooks")) return 1;
    snprintf(ext->error, sizeof ext->error, "unknown notebooks was not agreed");
    return 0;
}

int rx_nb_state(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, const char *state, int64_t count) {
    if (!rx_nb_agreed(ext)) return -1;
    char line[160], c[32] = "-";
    if (count >= 0) snprintf(c, sizeof c, "%lld", (long long)count);
    snprintf(line, sizeof line, "nb-state %lld %lld %lld %s %s", (long long)nb, (long long)cell, (long long)run, state, c);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

int rx_nb_output_send(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, const char *op, const char *target,
                      const void *body, size_t len) {
    if (!rx_nb_agreed(ext)) return -1;
    char line[256];
    snprintf(line, sizeof line, "nb-output %lld %lld %lld %s %s %zu", (long long)nb, (long long)cell, (long long)run,
             op, target && *target ? target : "-", len);
    return rx_request(ext, line, body, len, NULL, NULL);
}

int rx_nb_stream(rx_ext *ext, int64_t nb, int64_t cell, int64_t run, int is_stderr, const void *data, size_t len) {
    if (!rx_nb_agreed(ext)) return -1;
    const unsigned char *p = (const unsigned char *)data;
    while (len) {
        size_t n = len < 65536 ? len : 65536;
        while (n < len && n > 0 && (p[n] & 0xc0) == 0x80) n--;
        if (!n) n = len < 65536 ? len : 65536;
        char line[160];
        snprintf(line, sizeof line, "nb-stream %lld %lld %lld %s %zu", (long long)nb, (long long)cell, (long long)run,
                 is_stderr ? "stderr" : "stdout", n);
        if (rx_request(ext, line, p, n, NULL, NULL) < 0) return -1;
        p += n;
        len -= n;
    }
    return 0;
}

int rx_nb_kernel_label(rx_ext *ext, int64_t nb, const char *state, const char *label) {
    if (!rx_nb_agreed(ext)) return -1;
    char text[201], line[96];
    size_t n = 0;
    for (const char *p = label; *p && n < 200; p++) text[n++] = (unsigned char)*p < 32 ? ' ' : *p;
    while (n > 0 && n < strlen(label) && ((unsigned char)text[n] & 0xc0) == 0x80) n--;
    text[n] = 0;
    snprintf(line, sizeof line, "nb-kernel-label %lld %s %zu", (long long)nb, state, n);
    return rx_request(ext, line, text, n, NULL, NULL);
}

void rx_command_body(rx_buf *out, const char *program, const char *dir, const char *const *args, int argc,
                     const char *const *env, int envc) {
    rx_buf_fmt(out, "%zu %zu %d %d\n", strlen(program), strlen(dir), argc, envc);
    rx_buf_str(out, program);
    rx_buf_str(out, dir);
    for (int i = 0; i < argc + envc; i++) {
        const char *a = i < argc ? args[i] : env[i - argc];
        rx_buf_fmt(out, "%zu\n", strlen(a));
        rx_buf_str(out, a);
    }
}

int64_t rx_nb_exec(rx_ext *ext, int64_t nb, const char *program, const char *dir, const char *const *args, int argc,
                   const char *const *env, int envc) {
    if (!rx_nb_agreed(ext)) return -1;
    rx_buf b = {NULL, 0, 0};
    rx_command_body(&b, program, dir ? dir : "", args, argc, env, envc);
    char line[96];
    snprintf(line, sizeof line, "nb-exec %lld %zu", (long long)nb, b.len);
    int64_t v = rx_req_num(ext, line, b.data, b.len);
    rx_buf_free(&b);
    return v;
}

int rx_nb_exec_signal(rx_ext *ext, int64_t exec, const char *signal) {
    if (!rx_nb_agreed(ext)) return -1;
    char line[96];
    snprintf(line, sizeof line, "nb-exec-signal %lld %s", (long long)exec, signal);
    return rx_request(ext, line, NULL, 0, NULL, NULL);
}

/* nb-open and nb-save (f: ID VERB NB LEN) with their bodies */
static void rx_nb_answer(rx_ext *ext, const rx_handlers *h, void *user, char **f, const unsigned char *body, size_t len) {
    rx_buf out = {NULL, 0, 0}, line = {NULL, 0, 0};
    const char *why = NULL, *code = "refused";
    int known = rx_has(ext, "notebooks") && (strcmp(f[1], "nb-open") == 0 ? h->nb_open != NULL : h->nb_save != NULL);
    if (known) {
        /* PATH_LEN [DATA_LEN] LF PATH [DATA] */
        const unsigned char *nl = body ? (const unsigned char *)memchr(body, '\n', len) : NULL;
        char head[64] = "";
        size_t pl = 0, dl = 0;
        int is_open = f[1][3] == 'o', ok = 0;
        if (nl && (size_t)(nl - body) < sizeof head) {
            memcpy(head, body, (size_t)(nl - body));
            head[nl - body] = 0;
            char *sp = strchr(head, ' ');
            if (is_open && sp) {
                *sp = 0;
                ok = rx_nb_size(head, (long long)len, &pl) && rx_nb_size(sp + 1, (long long)len, &dl);
            } else if (!is_open && !sp) {
                ok = rx_nb_size(head, (long long)len, &pl);
            }
        }
        size_t at = nl ? (size_t)(nl - body) + 1 : 0;
        if (ok && pl <= len - at && (!is_open || dl == len - at - pl)) {
            char *path = (char *)malloc(pl + 1);
            if (!path) abort();
            memcpy(path, body + at, pl);
            path[pl] = 0;
            if (is_open) {
                rx_nb_open req = {atoll(f[0]), atoll(f[2]), path, body + at + pl, dl};
                why = h->nb_open(ext, &req, &out, user);
            } else {
                rx_nb_save req = {atoll(f[0]), atoll(f[2]), path, body + at + pl, len - at - pl};
                why = h->nb_save(ext, &req, &out, user);
            }
            free(path);
        } else {
            code = "syntax";
            why = "the body cannot be read";
        }
    }
    if (!known) {
        rx_buf_fmt(&line, "error %s unknown no such request", f[0]);
        rx_write(line.data, NULL, 0);
    } else if (why) {
        rx_buf_fmt(&line, "error %s %s ", f[0], code);
        for (const char *p = why; *p && line.len < 1000; p++) rx_buf_add(&line, *p == '\n' ? " " : p, 1);
        rx_write(line.data, NULL, 0);
    } else {
        rx_buf_fmt(&line, "ok %s %zu", f[0], out.len);
        rx_write(line.data, out.data, out.len);
    }
    rx_buf_free(&out);
    rx_buf_free(&line);
}

static void rx_view_dispatch(rx_ext *ext, const rx_handlers *h, void *user, char **f, int n) {
    if (n < 4) return;
    rx_view_event ev;
    memset(&ev, 0, sizeof ev);
    ev.kind = f[3];
    double v[16] = {0};
    for (int i = 4; i < n && i - 4 < 16; i++) v[i - 4] = rx_atof(f[i]);
    if (strcmp(ev.kind, "size") == 0) {
        ev.width = v[0], ev.height = v[1];
    } else if (strcmp(ev.kind, "metrics") == 0) {
        static const char *names[4] = {"small", "normal", "large", "mono"};
        for (int i = 4; i + 2 < n; i += 3)
            for (int k = 0; k < 4; k++)
                if (strcmp(f[i], names[k]) == 0) ev.advance[k] = rx_atof(f[i + 1]), ev.line_height[k] = rx_atof(f[i + 2]);
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
        } else if (ext->gone || !(m = rx_read_msg(ext))) {
            return 1;               /* nothing more can be read: the stream may be out of step */
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
            } else if (strcmp(f[1], "action") == 0 && h->action) {
                h->action(ext, m->body ? m->body : (const unsigned char *)"", m->body_len, user);
            } else if (strcmp(f[1], "nb-run") == 0 && n == 7 && h->nb_run && rx_has(ext, "notebooks")) {
                rx_nb_run run;
                run.nb = atoll(f[2]);
                run.cell = atoll(f[3]);
                run.run = atoll(f[4]);
                run.lang = f[5];
                run.source = m->body ? m->body : (const unsigned char *)"";
                run.len = m->body_len;
                h->nb_run(ext, &run, user);
            } else if (strcmp(f[1], "nb-exec-exit") == 0 && n == 4 && h->nb_exec_exit && rx_has(ext, "notebooks")) {
                h->nb_exec_exit(ext, atoll(f[2]), strcmp(f[3], "killed") == 0 ? -1 : atoi(f[3]), user);
            } else if (strncmp(f[1], "nb-", 3) == 0 && n == 3 && h->nb_event && rx_has(ext, "notebooks") &&
                       (strcmp(f[1], "nb-interrupt") == 0 || strcmp(f[1], "nb-restart") == 0 ||
                        strcmp(f[1], "nb-kernel") == 0 || strcmp(f[1], "nb-closed") == 0)) {
                h->nb_event(ext, f[1] + 3, atoll(f[2]), user);
            } else if (strcmp(f[1], "bye") == 0) {
                if (h->bye) h->bye(ext, user);
                done = 1;
            }
        } else if (n == 4 && f[0][0] >= '1' && f[0][0] <= '9' && rx_host_body(m->line)) {
            rx_nb_answer(ext, h, user, f, m->body, m->body_len);
        } else if (n >= 2 && f[0][0] >= '1' && f[0][0] <= '9') {
            rx_answer(ext, h, user, f, n, m->body, m->body_len);
        }
        free(copy);
        rx_free_msg(m);
        if (done) return 0;
    }
}

#endif /* RHUNEXT_IMPLEMENTATION */
