import React from 'react';
import { Box, Code, Text } from '@chakra-ui/react';

interface DiffLine {
  type: 'add' | 'remove' | 'context' | 'hunk' | 'header';
  content: string;
}

const parseDiff = (raw: string): DiffLine[] => {
  let diff = raw;
  const fenceMatch = diff.match(/^````?diff\n([\s\S]*?)````?$/);
  if (fenceMatch) {
    diff = fenceMatch[1];
  }

  return diff.split('\n').map((line): DiffLine => {
    if (line.startsWith('@@')) return { type: 'hunk', content: line };
    if (line.startsWith('---') || line.startsWith('+++'))
      return { type: 'header', content: line };
    if (line.startsWith('+')) return { type: 'add', content: line };
    if (line.startsWith('-')) return { type: 'remove', content: line };
    return { type: 'context', content: line };
  });
};

const lineStyles: Record<DiffLine['type'], { bg: string; color: string }> = {
  add: { bg: 'rgba(46, 160, 67, 0.15)', color: '#3fb950' },
  remove: { bg: 'rgba(248, 81, 73, 0.15)', color: '#f85149' },
  hunk: { bg: 'rgba(56, 139, 253, 0.10)', color: '#58a6ff' },
  header: { bg: 'transparent', color: '#8b949e' },
  context: { bg: 'transparent', color: 'inherit' },
};

interface DiffViewerProps {
  diff: string;
  filePath?: string;
}

const DiffViewer: React.FC<DiffViewerProps> = ({ diff, filePath }) => {
  const lines = parseDiff(diff);

  const displayPath =
    filePath ??
    (() => {
      const hdr = lines.find(
        (l) => l.type === 'header' && l.content.startsWith('---'),
      );
      if (hdr) {
        const m = hdr.content.match(/^---\s+(.+?)(?:\t.*)?$/);
        if (m) return m[1].replace(/^[ab]\//, '');
      }
      return undefined;
    })();

  return (
    <Box
      borderRadius="md"
      overflow="hidden"
      border="1px solid"
      borderColor="gray.200"
    >
      {displayPath && (
        <Box
          px={3}
          py={1.5}
          bg="gray.50"
          borderBottom="1px solid"
          borderColor="gray.200"
        >
          <Text
            fontSize="xs"
            fontFamily="mono"
            color="gray.500"
            fontWeight="medium"
          >
            {displayPath}
          </Text>
        </Box>
      )}
      <Code
        display="block"
        whiteSpace="pre"
        fontSize="xs"
        p={0}
        bg="gray.50"
        w="full"
        overflowX="auto"
      >
        {lines.map((line, i) => {
          const style = lineStyles[line.type];
          const key = `${line.type}:${line.content}:${String(i)}`;
          return (
            <Box
              key={key}
              px={3}
              py="1px"
              bg={style.bg}
              color={style.color}
              fontFamily="mono"
              fontSize="xs"
              lineHeight="tall"
              _first={{ pt: 1 }}
              _last={{ pb: 1 }}
            >
              {line.content || ' '}
            </Box>
          );
        })}
      </Code>
    </Box>
  );
};

export default DiffViewer;
