import { useCallback, useEffect, useRef } from 'react';
import type { Invocation } from '../api/AtryumAPI';
import { getAdminAccessToken, refreshAdminAccessToken } from '../auth/adminAuth';

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

const STREAM_URL = '/api/v1/admin/invocations/stream?status=pending_approval&limit=50';

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
    let isClosed = false;
    let retryDelayMs = 1000;
    let retryTimer: ReturnType<typeof window.setTimeout> | null = null;
    let controller: AbortController | null = null;

    const handleInvocations = (data: string) => {
      const payload = parseInvocationStreamPayload(data);
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

    const dispatchEvent = (eventName: string, dataLines: string[]) => {
      if (eventName !== 'invocations' || dataLines.length === 0) return;
      handleInvocations(dataLines.join('\n'));
    };

    const scheduleReconnect = () => {
      if (isClosed) return;
      const delay = retryDelayMs;
      retryDelayMs = Math.min(retryDelayMs * 2, 15000);
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void connect(false);
      }, delay);
    };

    const connect = async (didRefresh: boolean) => {
      if (isClosed) return;
      controller = new AbortController();
      try {
        let token = await getAdminAccessToken();
        const headers: HeadersInit = { Accept: 'text/event-stream' };
        if (token) headers.Authorization = `Bearer ${token}`;

        let response = await fetch(STREAM_URL, {
          headers,
          signal: controller.signal,
        });

        if (response.status === 401 && !didRefresh) {
          token = await refreshAdminAccessToken();
          if (token) {
            response = await fetch(STREAM_URL, {
              headers: {
                Accept: 'text/event-stream',
                Authorization: `Bearer ${token}`,
              },
              signal: controller.signal,
            });
          }
        }

        if (response.status === 401) return;
        if (!response.ok || !response.body) {
          scheduleReconnect();
          return;
        }

        retryDelayMs = 1000;
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        let eventName = 'message';
        let dataLines: string[] = [];

        const processLine = (rawLine: string) => {
          const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
          if (line === '') {
            dispatchEvent(eventName, dataLines);
            eventName = 'message';
            dataLines = [];
            return;
          }
          if (line.startsWith(':')) return;
          const separator = line.indexOf(':');
          const field = separator === -1 ? line : line.slice(0, separator);
          const rawValue = separator === -1 ? '' : line.slice(separator + 1);
          const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue;
          if (field === 'event') eventName = value;
          if (field === 'data') dataLines.push(value);
        };

        while (!isClosed) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split('\n');
          buffer = lines.pop() ?? '';
          for (const line of lines) processLine(line);
        }
        if (buffer !== '') processLine(buffer);
        scheduleReconnect();
      } catch (err) {
        if (!isClosed && !(err instanceof DOMException && err.name === 'AbortError')) {
          scheduleReconnect();
        }
      }
    };

    void connect(false);

    return () => {
      isClosed = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      controller?.abort();
    };
  }, [notifyPendingApprovals]);
};
