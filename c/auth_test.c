/* tests for rhunext.h's sign-in helpers: cc -std=c99 -Wall -Wextra -o auth-test auth_test.c && ./auth-test
 * (Windows: add -lws2_32 -lbcrypt) */
#ifndef _WIN32
#define _POSIX_C_SOURCE 200809L
#endif
#define RHUNEXT_IMPLEMENTATION
#define RHUNEXT_AUTH
#include "rhunext.h"
#include <assert.h>

static void hex32(const unsigned char *d, char *out) {
    for (int i = 0; i < 32; i++) snprintf(out + 2 * i, 3, "%02x", d[i]);
}

int main(void) {
    unsigned char sum[32];
    char hex[65];
    rx_sha256("abc", 3, sum);
    hex32(sum, hex);
    assert(strcmp(hex, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad") == 0);
    rx_sha256("", 0, sum);
    hex32(sum, hex);
    assert(strcmp(hex, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855") == 0);
    /* 56 bytes: the length goes into a second block */
    rx_sha256("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq", 56, sum);
    hex32(sum, hex);
    assert(strcmp(hex, "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1") == 0);
    char ch[44];
    rx_pkce_challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", ch);   /* RFC 7636 appendix B */
    assert(strcmp(ch, "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM") == 0);
    rx_pkce p;
    assert(rx_pkce_new(&p) == 0 && strlen(p.verifier) == 43);
    rx_pkce_challenge(p.verifier, ch);
    assert(strcmp(ch, p.challenge) == 0);
    char state[33];
    assert(rx_random_hex(state, 16) == 0 && strlen(state) == 32);
    char *v = rx_query_get("a=1&code=c%2B1+x&state=s", "code");
    assert(v && strcmp(v, "c+1 x") == 0);
    free(v);
    assert(rx_query_get("a=1", "code") == NULL);

    /* the browser's redirect: a client connects and writes before the listener accepts */
    rx_loopback lb;
    assert(rx_loopback_listen(&lb) == 0 && lb.port > 0);
    char uri[64];
    rx_loopback_redirect_uri(&lb, uri, sizeof uri);
    assert(strncmp(uri, "http://127.0.0.1:", 17) == 0);
    for (int round = 0; round < 3; round++) {
        intptr_t c = (intptr_t)socket(AF_INET, SOCK_STREAM, 0);
        struct sockaddr_in a;
        memset(&a, 0, sizeof a);
        a.sin_family = AF_INET;
        a.sin_addr.s_addr = htonl(0x7f000001);
        a.sin_port = htons((unsigned short)lb.port);
        assert(connect(c, (struct sockaddr *)&a, sizeof a) == 0);
        const char *req = round == 0 ? "GET /favicon.ico HTTP/1.1\r\nHost: x\r\n\r\n"
                        : round == 1 ? "GET /callback?code=1&state=wrong HTTP/1.1\r\n\r\n"
                                     : "GET /callback?code=c%2B1&state=xyz HTTP/1.1\r\nHost: x\r\n\r\n";
        send(c, req, (int)strlen(req), 0);
        char *q = NULL;
        if (round == 0) {
            /* not the callback: answered 404, and the wait goes on (to its timeout here) */
            assert(rx_loopback_wait(&lb, "xyz", 300, &q) == 0);
        } else if (round == 1) {
            assert(rx_loopback_wait(&lb, "xyz", 5000, &q) == -1);
        } else {
            assert(rx_loopback_wait(&lb, "xyz", 5000, &q) == 1 && strcmp(q, "code=c%2B1&state=xyz") == 0);
            free(q);
        }
        char resp[1024];
        int n = 0, got;
        while ((got = (int)recv(c, resp + n, (int)(sizeof resp - 1 - n), 0)) > 0) n += got;
        resp[n] = 0;
        assert(round != 0 || strstr(resp, " 404 "));
        assert(round != 1 || strstr(resp, "Sign-in failed"));
        assert(round != 2 || strstr(resp, "Signed in. You can close this tab"));
        RX_CLOSESOCK(c);
    }
    rx_loopback_close(&lb);
    fputs("c auth ok\n", stderr);
    return 0;
}
