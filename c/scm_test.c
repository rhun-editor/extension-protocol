/* cc -std=c99 -Wall -Wextra -o /tmp/rhun-scm-c scm_test.c && /tmp/rhun-scm-c */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>

static void fill(rx_buf *b, const char *s) { rx_buf_add(b, s, strlen(s)); }

int main(void) {
    rx_git_repo_info repo = {0};
    fill(&repo.body, "4 4 11 branch 1 2 dirty\n/tmpmainorigin/main");
    assert(rx_parse_git_repo(&repo) == 0 && repo.root_len == 4 && !memcmp(repo.branch, "main", 4) &&
           repo.upstream_len == 11 && !strcmp(repo.head, "branch") && repo.ahead == 1 && repo.behind == 2 &&
           !strcmp(repo.state, "dirty"));
    rx_git_repo_free(&repo);
    fill(&repo.body, "4 4 11 branch 1 2 dirty\n/tmpmainorigin/mainx");
    assert(rx_parse_git_repo(&repo) == -1);
    rx_git_repo_free(&repo);

    rx_git_status_list st = {0};
    fill(&st.body, "2\n.M 5\na.txt?? 12\nnew file.txt");
    assert(rx_parse_git_status(&st) == 0 && st.n == 2 && st.entries[0].x == '.' && st.entries[0].y == 'M' &&
           st.entries[1].path_len == 12);
    rx_git_status_free(&st);

    char body[512];
    const char *z = "0000000000000000000000000000000000000000", *a = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    snprintf(body, sizeof body, "2 2\n%s 100 3 7 5\nAdaada@x.yFirst%s 200 3 3 4\nNotnotWork%s 1 2 1\n%s 3 3 3\n", a, z, a, z);
    rx_blame bl = {0};
    fill(&bl.body, body);
    assert(rx_parse_blame(&bl) == 0 && bl.ncommits == 2 && bl.nruns == 2 && bl.commits[0].mail_len == 7 &&
           !strcmp(bl.runs[1].sha, z) && bl.runs[0].l2 == 2);
    rx_blame_free(&bl);

    snprintf(body, sizeof body, "2\n%s %s 10 3 4\nAdaMore%s - 9 3 5\nAdaFirst", a, z, z);
    rx_log lg = {0};
    fill(&lg.body, body);
    assert(rx_parse_log(&lg) == 0 && lg.n == 2 && lg.entries[0].parents_len == 40 && lg.entries[1].parents_len == 0 &&
           lg.entries[1].subject_len == 5);
    rx_log_free(&lg);

    rx_commit_message_ask req;
    assert(rx_parse_commit_message(3, (const unsigned char *)"4 6\nmain+a\n-b\n", 14, &req) == 0 &&
           req.branch_len == 4 && req.diff_len == 6);
    assert(rx_parse_commit_message(3, (const unsigned char *)"4 7\nmain+a\n-b\n", 14, &req) == -1);
    size_t s, n;
    assert(rx_check_commit_message("  Add x\n", 8, &s, &n) == 0 && s == 2 && n == 5);
    assert(rx_check_commit_message("\x1b[31m", 5, &s, &n) == -1);
    assert(rx_valid_rev("HEAD~2") && rx_valid_rev("main@{1}") && !rx_valid_rev("-x") && !rx_valid_rev("a:b"));

    char *fields[] = {"changed"};
    rx_event ev = {0};
    ev.name = "git", ev.argc = 1, ev.argv = fields;
    assert(rx_is_git_changed(&ev));
    rx_ext ext = {0};
    rx_git_repo_info r2;
    assert(rx_git_repo(&ext, &r2) == -1 && strstr(rx_error(&ext), "unknown"));

    /* an scm-update body */
    rx_buf u = {NULL, 0, 0};
    rx_scm_head(&u, "default", "Message", 1, 1);
    rx_scm_action(&u, "check", "commit", NULL, 0, "Commit");
    rx_scm_group(&u, "changes", "Changes", 1, 2);
    rx_scm_action(&u, NULL, "revert_all", NULL, 0, NULL);
    rx_scm_res(&u, 'M', "modified", "a.txt", 2);
    rx_scm_click(&u, "open_change", "a.txt", 5);
    rx_scm_action(&u, NULL, "revert", "a.txt", 5, "Revert");
    rx_scm_res(&u, 'R', NULL, "gone.txt", 1);
    rx_scm_tip(&u, "renamed");
    const char *want = "7 7 1 1\ndefaultMessage"
                       "action check commit 0 6\nCommit"
                       "group changes 7 1 2\nChanges" "action - revert_all 0 0\n"
                       "res M modified 5 2\na.txt" "command open_change 5\na.txt" "action - revert 5 6\na.txtRevert"
                       "res R - 8 1\ngone.txt" "tip 7\nrenamed";
    assert(u.len == strlen(want) && !memcmp(u.data, want, u.len));
    rx_buf_free(&u);
    assert(rx_scm_register(&ext, "/r", "Hg") == -1 && strstr(rx_error(&ext), "unknown"));
    return 0;
}
