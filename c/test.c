/* tests for rhunext.h: cc -std=c99 -Wall -Wextra -o test test.c && ./test */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>
#include <locale.h>
#include <stdio.h>
int main(void) {
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
    char *doc = malloc(70001); memset(doc, 'a', 70000); doc[69990] = (char)0xc3; doc[69991] = (char)0xa9; doc[70000] = 0;
    rx_signature_answer(&s, "f(x)", 2, 3, doc);
    assert(s.len <= 65536);
    rx_buf s2 = {NULL, 0, 0};
    char *sig = malloc(70001); memset(sig, 'x', 70000); sig[70000] = 0;
    rx_signature_answer(&s2, sig, -1, -1, "d");
    assert(s2.len == 0);
    puts("c ok");
    return 0;
}
