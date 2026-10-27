#import <Cocoa/Cocoa.h>
#include "ui_darwin.h"
#include "_cgo_export.h"

@interface AlborzDesktop : NSObject <NSApplicationDelegate, NSMenuDelegate>
@property(strong) NSStatusItem *statusItem;
@property(strong) NSMenuItem *codeItem;
@end

static NSString *setupCode(void) {
    char *code = alborz_code();
    NSString *text = [NSString stringWithUTF8String:code];
    free(code);
    return text;
}

@implementation AlborzDesktop
- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
    NSImage *icon = [[NSApp applicationIconImage] copy];
    icon.size = NSMakeSize(18, 18);
    self.statusItem.button.image = icon;
    self.statusItem.button.toolTip = @"Alborz mail";
    NSMenu *menu = [[NSMenu alloc] init];
    menu.delegate = self;
    NSMenuItem *open = [menu addItemWithTitle:@"Open Alborz" action:@selector(open:) keyEquivalent:@""];
    open.target = self;
    self.codeItem = [menu addItemWithTitle:@"Copy setup code" action:@selector(copyCode:) keyEquivalent:@""];
    self.codeItem.target = self;
    [menu addItem:[NSMenuItem separatorItem]];
    NSMenuItem *quit = [menu addItemWithTitle:@"Quit Alborz" action:@selector(quit:) keyEquivalent:@"q"];
    quit.target = self;
    self.statusItem.menu = menu;
    NSString *code = setupCode();
    if (code.length) {
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"Set up Alborz";
        alert.informativeText = @"Enter this one-time code in the setup page. Alborz stays in your menu bar while you use it in your browser.";
        NSTextField *field = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 340, 30)];
        field.stringValue = code;
        field.editable = NO;
        field.selectable = YES;
        field.font = [NSFont monospacedSystemFontOfSize:16 weight:NSFontWeightRegular];
        alert.accessoryView = field;
        [alert addButtonWithTitle:@"Copy code and open"];
        [alert addButtonWithTitle:@"Open without copying"];
        [NSApp activateIgnoringOtherApps:YES];
        if ([alert runModal] == NSAlertFirstButtonReturn) { [self copyCode:nil]; }
    }
    alborz_open();
}
- (void)menuWillOpen:(NSMenu *)menu { self.codeItem.hidden = setupCode().length == 0; }
- (BOOL)applicationShouldHandleReopen:(NSApplication *)application hasVisibleWindows:(BOOL)visible {
    alborz_open();
    return NO;
}
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)application {
    // Close the local server before Cocoa terminates the process.
    alborz_quit();
    return NSTerminateNow;
}
- (void)open:(id)sender { alborz_open(); }
- (void)copyCode:(id)sender {
    NSString *code = setupCode();
    if (code.length) {
        [[NSPasteboard generalPasteboard] clearContents];
        [[NSPasteboard generalPasteboard] setString:code forType:NSPasteboardTypeString];
    }
}
- (void)quit:(id)sender { [NSApp terminate:nil]; }
@end

void alborz_run(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        AlborzDesktop *delegate = [[AlborzDesktop alloc] init];
        NSApp.delegate = delegate;
        [NSApp run];
        [[NSStatusBar systemStatusBar] removeStatusItem:delegate.statusItem];
        NSApp.delegate = nil;
    }
}

void alborz_stop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (NSApp.modalWindow) { [NSApp abortModal]; }
        [NSApp stop:nil];
        NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:event atStart:NO];
    });
}

void alborz_error(const char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = @"Alborz could not continue";
        alert.informativeText = [NSString stringWithUTF8String:message];
        alert.alertStyle = NSAlertStyleCritical;
        [alert addButtonWithTitle:@"OK"];
        [NSApp activateIgnoringOtherApps:YES];
        [alert runModal];
    }
}
