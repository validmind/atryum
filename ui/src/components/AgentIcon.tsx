import React, { useCallback, useState } from 'react';
import { Box, Image } from '@chakra-ui/react';
import { SparklesIcon } from '@heroicons/react/24/outline';

export type AgentKind =
  | 'amp'
  | 'codex'
  | 'cursor'
  | 'claude'
  | 'claude-code'
  | 'claude-desktop'
  | 'copilot'
  | 'agentforce'
  | 'opencode'
  | 'zed'
  | 'cline'
  | 'windsurf'
  | 'aider'
  | 'unknown';

const KIND_LABEL: Record<AgentKind, string> = {
  amp: 'Amp',
  codex: 'Codex',
  cursor: 'Cursor',
  claude: 'Claude',
  'claude-code': 'Claude Code',
  'claude-desktop': 'Claude Desktop',
  copilot: 'GitHub Copilot',
  agentforce: 'Agentforce',
  opencode: 'opencode',
  zed: 'Zed',
  cline: 'Cline',
  windsurf: 'Windsurf',
  aider: 'Aider',
  unknown: 'Unknown agent',
};

const AGENT_ICON_FILES: Record<AgentKind, string | null> = {
  amp: 'amp.svg',
  codex: 'codex.svg',
  cursor: 'cursor.svg',
  claude: 'claude.svg',
  'claude-code': 'claude-code.svg',
  'claude-desktop': 'claude-desktop.svg',
  copilot: 'copilot.svg',
  agentforce: 'agentforce.svg',
  opencode: 'opencode.svg',
  zed: 'zed.svg',
  cline: 'cline.svg',
  windsurf: 'windsurf.svg',
  aider: 'aider.jpg',
  unknown: null,
};

export function detectAgentKind(name: string | null | undefined): AgentKind {
  if (!name) return 'unknown';
  const n = name.toLowerCase().trim();
  if (n === 'amp' || n.startsWith('amp-') || n.includes('sourcegraph'))
    return 'amp';
  if (n === 'codex' || n.startsWith('codex-')) return 'codex';
  if (n.includes('cursor')) return 'cursor';
  if (n === 'claude-code' || n.includes('claude code')) return 'claude-code';
  if (n.includes('claude-desktop') || n.includes('claude desktop'))
    return 'claude-desktop';
  if (n.includes('claude')) return 'claude';
  if (n.includes('copilot') || n.includes('github')) return 'copilot';
  if (n.includes('agentforce') || n.includes('salesforce')) return 'agentforce';
  if (
    n === 'opencode' ||
    n.startsWith('opencode-') ||
    n.includes('opencode') ||
    n === 'open-code' ||
    n === 'open code'
  )
    return 'opencode';
  if (n === 'zed' || n.startsWith('zed-') || n.includes('zed industries'))
    return 'zed';
  if (n === 'cline' || n.startsWith('cline-')) return 'cline';
  if (n.includes('windsurf') || n.includes('codeium')) return 'windsurf';
  if (n === 'aider' || n.startsWith('aider-')) return 'aider';
  return 'unknown';
}

interface AgentIconProps {
  name?: string | null;
  kind?: AgentKind;
  size?: number;
}

const AgentIcon: React.FC<AgentIconProps> = ({ name, kind, size = 18 }) => {
  const resolved = kind ?? detectAgentKind(name);
  const label = KIND_LABEL[resolved];
  const iconFile = AGENT_ICON_FILES[resolved];
  const [imgFailed, setImgFailed] = useState(false);
  const handleImgError = useCallback(() => setImgFailed(true), []);
  const showImage = iconFile !== null && !imgFailed;

  return (
    <Box
      as="span"
      title={label}
      aria-label={label}
      role="img"
      display="inline-flex"
      alignItems="center"
      justifyContent="center"
      w={`${size}px`}
      h={`${size}px`}
      flexShrink={0}
    >
      {showImage ? (
        <Image
          src={`/atryum-agent-icons/${iconFile}`}
          alt={label}
          boxSize={`${size}px`}
          objectFit="contain"
          onError={handleImgError}
        />
      ) : (
        renderGlyph(resolved, size)
      )}
    </Box>
  );
};

function renderGlyph(kind: AgentKind, size: number) {
  switch (kind) {
    case 'amp':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <path
            d="M13.5 2 4 14h6l-1.5 8L20 9h-6.5L15 2h-1.5Z"
            fill="#F97316"
            stroke="#C2410C"
            strokeWidth="1"
            strokeLinejoin="round"
          />
        </svg>
      );
    case 'codex':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <rect x="2" y="4" width="20" height="16" rx="3" fill="#0F172A" stroke="#475569" strokeWidth="1" />
          <path d="m6 10 3 2-3 2M11 15h7" stroke="#10B981" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case 'cursor':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <path d="M4 3v16l4.5-4 2.5 6 2.5-1-2.5-6H17L4 3Z" fill="#0EA5E9" stroke="#0369A1" strokeWidth="1" strokeLinejoin="round" />
        </svg>
      );
    case 'claude':
    case 'claude-code':
    case 'claude-desktop':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="5" fill="#D97757" />
          {[0, 45, 90, 135, 180, 225, 270, 315].map((deg) => (
            <rect key={deg} x="11" y="2" width="2" height="4" rx="1" fill="#D97757" transform={`rotate(${deg} 12 12)`} />
          ))}
        </svg>
      );
    case 'copilot':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <circle cx="12" cy="12" r="7" fill="#111827" />
          <circle cx="9.5" cy="11" r="1.2" fill="#fff" />
          <circle cx="14.5" cy="11" r="1.2" fill="#fff" />
          <path d="M9 14.5c1 1 2 1.5 3 1.5s2-.5 3-1.5" stroke="#fff" strokeWidth="1.2" strokeLinecap="round" />
        </svg>
      );
    case 'opencode':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <path d="m8.5 7-5 5 5 5M15.5 7l5 5-5 5M13 5l-2 14" stroke="#10B981" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case 'agentforce':
      return (
        <svg width={size} height={size} viewBox="0 0 24 24" fill="none">
          <path d="M7 18a4 4 0 0 1-.5-7.97A6 6 0 0 1 18 11a3.5 3.5 0 0 1-1 7H7Z" fill="#00A1E0" />
          <path d="m12 9-2 4h2l-1 4 3-5h-2l1-3h-1Z" fill="#FACC15" stroke="#A16207" strokeWidth="0.5" strokeLinejoin="round" />
        </svg>
      );
    default:
      return (
        <SparklesIcon width={size} height={size} color="#7C3AED" strokeWidth={1.75} />
      );
  }
}

export default AgentIcon;
