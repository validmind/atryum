import { useCallback, useEffect, useRef } from 'react';
import type { Invocation } from '../api/AtryumAPI';

interface InvocationStreamPayload {
  items?: Invocation[];
}

const NOTIFICATION_TITLE = 'Atryum approval needed';
const NOTIFICATION_ICON = '/ui/atryum-notification-icon.svg';

const buildNotificationBody = (invocation: Invocation): string => {
  const parts = [invocation.agent_id, invocation.server_name, invocation.tool_name]
    .map((part) => part?.trim())
    .filter(Boolean);
  return parts.length > 0
    ? parts.join(' / ')
    : 'An agent is waiting for human approval.';
};

const focusInvocations = (invocationId: string) => {
  const targetPath = `/ui/invocations`;
  window.focus();
  if (window.location.pathname !== targetPath) {
    // Full navigation; the page reads the hash on mount.
    window.location.assign(`${targetPath}#${invocationId}`);
    return;
  }
  // Already on the page: assigning the hash fires a `hashchange` event that
  // the page listens for. `history.replaceState` would not.
  window.location.hash = invocationId;
};

const showNotification = (invocation: Invocation) => {
  const notification = new Notification(NOTIFICATION_TITLE, {
    body: buildNotificationBody(invocation),
    icon: NOTIFICATION_ICON,
    tag: `atryum-approval-${invocation.invocation_id}`,
  });
  notification.onclick = () => focusInvocations(invocation.invocation_id);
};

const parseInvocationStreamPayload = (
  data: string,
): InvocationStreamPayload | null => {
  try {
    return JSON.parse(data) as InvocationStreamPayload;
  } catch {
    return null;
  }
};

const ensurePermissionAfterUserGesture = (onPermissionGranted: () => void) => {
  if (!('Notification' in window) || Notification.permission !== 'default') {
    return () => {};
  }

  const requestPermission = async () => {
    const permission = await Notification.requestPermission();
    if (permission === 'granted') {
      onPermissionGranted();
    }
    cleanup();
  };
  const cleanup = () => {
    window.removeEventListener('click', requestPermission);
    window.removeEventListener('keydown', requestPermission);
    window.removeEventListener('pointerdown', requestPermission);
  };

  window.addEventListener('click', requestPermission, { once: true });
  window.addEventListener('keydown', requestPermission, { once: true });
  window.addEventListener('pointerdown', requestPermission, { once: true });

  return cleanup;
};

export const useApprovalNotifications = () => {
  const pendingInvocations = useRef<Map<string, Invocation>>(new Map());
  const notifiedIds = useRef<Set<string>>(new Set());

  const notifyPendingApprovals = useCallback(() => {
    if (!('Notification' in window) || Notification.permission !== 'granted') {
      return;
    }

    for (const invocation of pendingInvocations.current.values()) {
      if (notifiedIds.current.has(invocation.invocation_id)) continue;

      showNotification(invocation);
      notifiedIds.current.add(invocation.invocation_id);
    }
  }, []);

  useEffect(
    () => ensurePermissionAfterUserGesture(notifyPendingApprovals),
    [notifyPendingApprovals],
  );

  useEffect(() => {
    if (!('EventSource' in window)) return;

    const source = new EventSource(
      '/api/v1/admin/invocations/stream?status=pending_approval&limit=50',
    );

    const handleInvocations = (event: MessageEvent<string>) => {
      const payload = parseInvocationStreamPayload(event.data);
      if (!payload) return;

      const pendingItems = (payload.items ?? []).filter(
        (item) => item.status === 'pending_approval',
      );
      pendingInvocations.current = new Map(
        pendingItems.map((item) => [item.invocation_id, item]),
      );

      // Drop notified IDs that are no longer pending so the set doesn't grow
      // unbounded over a long-lived session.
      for (const id of notifiedIds.current) {
        if (!pendingInvocations.current.has(id)) {
          notifiedIds.current.delete(id);
        }
      }

      notifyPendingApprovals();
    };

    source.addEventListener('invocations', handleInvocations as EventListener);

    return () => {
      source.removeEventListener(
        'invocations',
        handleInvocations as EventListener,
      );
      source.close();
    };
  }, [notifyPendingApprovals]);
};
