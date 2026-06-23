import { useEffect, useRef } from 'react';
import type { Invocation } from '../api/AtryumAPI';

interface InvocationStreamPayload {
  items?: Invocation[];
}

const NOTIFICATION_TITLE = 'Atryum approval needed';

const buildNotificationBody = (invocation: Invocation): string => {
  const parts = [invocation.agent_id, invocation.server_name, invocation.tool_name]
    .map((part) => part?.trim())
    .filter(Boolean);
  return parts.length > 0
    ? parts.join(' / ')
    : 'An agent is waiting for human approval.';
};

const notificationIcon = (): string | undefined => {
  return '/ui/atryum-logo-favicon.svg';
};

const focusInvocations = (invocationId: string) => {
  const targetPath = `/ui/invocations`;
  const targetUrl = `${targetPath}#${invocationId}`;
  window.focus();
  if (window.location.pathname !== targetPath) {
    window.location.assign(targetUrl);
    return;
  }
  window.history.replaceState(null, '', targetUrl);
};

const ensurePermissionAfterUserGesture = () => {
  if (!('Notification' in window) || Notification.permission !== 'default') {
    return () => {};
  }

  const requestPermission = () => {
    void Notification.requestPermission();
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
  const knownPendingIds = useRef<Set<string>>(new Set());
  const hasSeenInitialPayload = useRef(false);

  useEffect(() => ensurePermissionAfterUserGesture(), []);

  useEffect(() => {
    if (!('EventSource' in window)) return;

    const source = new EventSource(
      '/api/v1/admin/invocations/stream?status=pending_approval&limit=50',
    );

    const handleInvocations = (event: MessageEvent<string>) => {
      const payload = JSON.parse(event.data) as InvocationStreamPayload;
      const pendingItems = (payload.items ?? []).filter(
        (item) => item.status === 'pending_approval',
      );
      const nextPendingIds = new Set(
        pendingItems.map((item) => item.invocation_id),
      );

      if (!hasSeenInitialPayload.current) {
        knownPendingIds.current = nextPendingIds;
        hasSeenInitialPayload.current = true;
        return;
      }

      if (!('Notification' in window) || Notification.permission !== 'granted') {
        knownPendingIds.current = nextPendingIds;
        return;
      }

      for (const invocation of pendingItems) {
        if (knownPendingIds.current.has(invocation.invocation_id)) continue;

        const notification = new Notification(NOTIFICATION_TITLE, {
          body: buildNotificationBody(invocation),
          icon: notificationIcon(),
          tag: `atryum-approval-${invocation.invocation_id}`,
        });
        notification.onclick = () => focusInvocations(invocation.invocation_id);
      }

      knownPendingIds.current = nextPendingIds;
    };

    source.addEventListener('invocations', handleInvocations as EventListener);

    return () => {
      source.removeEventListener(
        'invocations',
        handleInvocations as EventListener,
      );
      source.close();
    };
  }, []);
};
