//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void setDockVisible(int visible, const void *iconData, int iconLen) {
	@autoreleasepool {
		NSData *copied = (iconData != NULL && iconLen > 0)
			? [NSData dataWithBytes:iconData length:(NSUInteger)iconLen]
			: nil;
		dispatch_block_t block = ^{
			static NSImage *cachedIcon = nil;
			if (cachedIcon == nil && copied != nil) {
				cachedIcon = [[NSImage alloc] initWithData:copied];
			}
			NSApplicationActivationPolicy policy = visible
				? NSApplicationActivationPolicyRegular
				: NSApplicationActivationPolicyAccessory;
			[NSApp setActivationPolicy:policy];
			if (visible) {
				if (cachedIcon != nil) {
					[NSApp setApplicationIconImage:cachedIcon];
				}
				[NSApp activateIgnoringOtherApps:YES];
			}
		};
		if ([NSThread isMainThread]) {
			block();
		} else {
			dispatch_async(dispatch_get_main_queue(), block);
		}
	}
}
*/
import "C"
import "unsafe"

func setDockIconVisible(visible bool, icon []byte) {
	var ptr unsafe.Pointer
	var length C.int
	if len(icon) > 0 {
		ptr = unsafe.Pointer(&icon[0])
		length = C.int(len(icon))
	}
	if visible {
		C.setDockVisible(1, ptr, length)
	} else {
		C.setDockVisible(0, nil, 0)
	}
}
