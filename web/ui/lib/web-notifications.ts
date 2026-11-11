export type WebNotificationPermission = NotificationPermission | "unsupported";

export function webNotificationPermission(): WebNotificationPermission {
  if (typeof window === "undefined" || !("Notification" in window)) return "unsupported";
  return Notification.permission;
}

/** Request permission only from a user gesture; browsers reject background prompts. */
export function requestWebNotificationPermission(): Promise<WebNotificationPermission> {
  if (typeof window === "undefined" || !("Notification" in window)) {
    return Promise.resolve("unsupported");
  }
  return Notification.requestPermission();
}

export function showWebNotification(
  title: string,
  options: NotificationOptions & { onClick?: () => void } = {},
): Notification | null {
  if (webNotificationPermission() !== "granted") return null;
  const { onClick, ...notificationOptions } = options;
  const notification = new Notification(title, notificationOptions);
  if (onClick) notification.onclick = onClick;
  return notification;
}
