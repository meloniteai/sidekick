//go:build darwin

#import <Cocoa/Cocoa.h>
#include <unistd.h>

static int sidekickActionFD = -1;

@interface SIDEKICKMenuController : NSObject
@property(strong) NSStatusItem *statusItem;
@property(strong) NSMenu *menu;
- (void)setup;
- (void)updateWithJSON:(NSString *)json;
- (void)performAction:(id)sender;
- (void)stopApp;
@end

static SIDEKICKMenuController *sidekickController = nil;

static NSImage *SIDEKICKToneImage(NSString *tone) {
    if (tone == nil || [tone length] == 0) {
        return nil;
    }

    NSColor *color = nil;
    if ([tone isEqualToString:@"good"]) {
        color = [NSColor systemGreenColor];
    } else if ([tone isEqualToString:@"ok"]) {
        color = [NSColor systemYellowColor];
    } else if ([tone isEqualToString:@"warn"]) {
        color = [NSColor systemOrangeColor];
    } else if ([tone isEqualToString:@"bad"]) {
        color = [NSColor systemRedColor];
    } else if ([tone isEqualToString:@"running"]) {
        color = [NSColor systemPurpleColor];
    } else if ([tone isEqualToString:@"accent"]) {
        color = [NSColor controlAccentColor];
    } else if ([tone isEqualToString:@"muted"]) {
        color = [NSColor tertiaryLabelColor];
    }
    if (color == nil) {
        return nil;
    }

    NSImage *image = [[NSImage alloc] initWithSize:NSMakeSize(9, 9)];
    [image lockFocus];
    [color setFill];
    NSBezierPath *path = [NSBezierPath bezierPathWithOvalInRect:NSMakeRect(1, 1, 7, 7)];
    [path fill];
    [image unlockFocus];
    [image setTemplate:NO];
    return image;
}

@implementation SIDEKICKMenuController

- (void)setup {
    self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    self.statusItem.button.title = @"Sidekick";
    self.statusItem.button.toolTip = @"Sidekick session status";
    self.menu = [[NSMenu alloc] initWithTitle:@"Sidekick"];
    self.statusItem.menu = self.menu;

    NSMenuItem *starting = [[NSMenuItem alloc] initWithTitle:@"Starting Sidekick..." action:nil keyEquivalent:@""];
    [starting setEnabled:NO];
    [self.menu addItem:starting];
}

- (void)updateWithJSON:(NSString *)json {
    NSData *data = [json dataUsingEncoding:NSUTF8StringEncoding];
    if (data == nil) {
        return;
    }

    NSError *error = nil;
    NSDictionary *root = [NSJSONSerialization JSONObjectWithData:data options:0 error:&error];
    if (error != nil || ![root isKindOfClass:[NSDictionary class]]) {
        return;
    }

    NSString *title = root[@"title"];
    if ([title isKindOfClass:[NSString class]] && [title length] > 0) {
        self.statusItem.button.title = title;
    }

    NSArray *items = root[@"items"];
    if (![items isKindOfClass:[NSArray class]]) {
        return;
    }

    [self.menu removeAllItems];
    for (NSDictionary *entry in items) {
        if (![entry isKindOfClass:[NSDictionary class]]) {
            continue;
        }

        NSNumber *separator = entry[@"separator"];
        if ([separator boolValue]) {
            [self.menu addItem:[NSMenuItem separatorItem]];
            continue;
        }

        NSString *itemTitle = entry[@"title"];
        if (![itemTitle isKindOfClass:[NSString class]] || [itemTitle length] == 0) {
            continue;
        }

        NSNumber *action = entry[@"action"];
        NSNumber *enabled = entry[@"enabled"];
        NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:itemTitle action:nil keyEquivalent:@""];
        if ([action integerValue] > 0) {
            item.target = self;
            item.action = @selector(performAction:);
            item.representedObject = action;
            item.enabled = [enabled boolValue];
        } else {
            item.enabled = NO;
        }

        NSString *tone = entry[@"tone"];
        item.image = SIDEKICKToneImage(tone);
        [self.menu addItem:item];
    }
}

- (void)performAction:(id)sender {
    NSMenuItem *item = (NSMenuItem *)sender;
    NSInteger action = [item.representedObject integerValue];
    if (sidekickActionFD >= 0 && action > 0 && action < 256) {
        unsigned char b = (unsigned char)action;
        (void)write(sidekickActionFD, &b, 1);
    }
}

- (void)stopApp {
    [NSApp stop:nil];
    NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                        location:NSZeroPoint
                                   modifierFlags:0
                                       timestamp:0
                                    windowNumber:0
                                         context:nil
                                         subtype:0
                                           data1:0
                                           data2:0];
    [NSApp postEvent:event atStart:YES];
}

@end

void SIDEKICKSetActionFD(int fd) {
    sidekickActionFD = fd;
}

void SIDEKICKRun(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [NSApp setAppearance:[NSAppearance appearanceNamed:NSAppearanceNameAqua]];

        sidekickController = [[SIDEKICKMenuController alloc] init];
        [sidekickController setup];
        [NSApp run];
    }
}

void SIDEKICKUpdateMenu(const char *json) {
    if (json == NULL || sidekickController == nil) {
        return;
    }
    NSString *payload = [NSString stringWithUTF8String:json];
    [sidekickController performSelectorOnMainThread:@selector(updateWithJSON:)
                                    withObject:payload
                                 waitUntilDone:NO];
}

void SIDEKICKStop(void) {
    if (sidekickController == nil) {
        return;
    }
    [sidekickController performSelectorOnMainThread:@selector(stopApp)
                                    withObject:nil
                                 waitUntilDone:NO];
}
