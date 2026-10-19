/* cc -std=c99 -Wall -Wextra -o /tmp/rhun-watch-c watch_test.c && /tmp/rhun-watch-c */
#define RHUNEXT_IMPLEMENTATION
#include "rhunext.h"
#include <assert.h>

int main(void) {
    char *fields[] = {"7", "changed"};
    rx_event event = {0};
    event.name = "watch";
    event.argc = 2;
    event.argv = fields;
    int64_t watch = 0;
    int rescan = -1;
    assert(rx_parse_workspace_watch(&event, &watch, &rescan) == 1 && watch == 7 && rescan == 0);
    fields[1] = "rescan";
    assert(rx_parse_workspace_watch(&event, &watch, &rescan) == 1 && watch == 7 && rescan == 1);
    const char *invalid[] = {"", "0", "-1", "+7", "2147483648", "999999999999999999999"};
    for (size_t i = 0; i < sizeof invalid / sizeof invalid[0]; i++) {
        fields[0] = (char *)invalid[i];
        assert(rx_parse_workspace_watch(&event, &watch, &rescan) == 0);
    }
    fields[0] = "7";
    fields[1] = "unknown";
    assert(rx_parse_workspace_watch(&event, &watch, &rescan) == 0);
    assert(rx_parse_workspace_watch(NULL, &watch, &rescan) == 0);
    rx_ext ext = {0};
    assert(rx_watch_workspace(&ext) == -1 && strstr(rx_error(&ext), "unknown"));
    assert(rx_unwatch_workspace(&ext, 7) == -1);
    strcpy(ext.features, "workspace_watch");
    assert(rx_unwatch_workspace(&ext, 0) == -1 && strstr(rx_error(&ext), "syntax"));
    assert(rx_unwatch_workspace(&ext, 2147483648LL) == -1);
    return 0;
}
