import React from 'react';
import {
  Box,
  Code,
  Divider,
  Heading,
  Link,
  ListItem,
  OrderedList,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
  UnorderedList,
} from '@chakra-ui/react';
import ReactMarkdown, { type Components } from 'react-markdown';
import remarkGfm from 'remark-gfm';

// CharterMarkdown renders charter text as markdown.
//
// Charter text is not authored in this UI: for ValidMind-synced agents it comes
// back over the wire from the backend's /charter-preview endpoint, which reads a
// ValidMind custom field edited on the far side of a different trust boundary.
// It is therefore treated as untrusted remote content, and the rendering path is
// deliberately built so that no markup in that text can ever reach the DOM:
//
//   1. react-markdown emits a React element tree, never an HTML string, so every
//      text node goes through React's normal escaping. There is no
//      dangerouslySetInnerHTML anywhere in this path.
//   2. Raw HTML embedded in the markdown source is escaped to visible text —
//      react-markdown only turns it into live markup if `rehype-raw` is added to
//      rehypePlugins. DO NOT ADD IT HERE. Note that `skipHtml` is deliberately
//      NOT set: it is equally safe but silently DELETES the text, so a charter
//      clause like "deny writes to <customers>" would lose the table name.
//      Escaping keeps the clause readable and is just as inert.
//   3. Link/image URLs run through defaultUrlTransform (react-markdown's default),
//      which strips javascript:, data: and other non-safelisted protocols.
//   4. disallowedElements drops the embedding elements outright, so even a future
//      plugin change cannot introduce a network-fetching or scripting node.
const DISALLOWED = ['script', 'style', 'iframe', 'object', 'embed', 'form', 'input', 'img'];

// react-markdown passes each custom component the source `node`. Spreading that
// straight onto a DOM element emits a bogus node="[object Object]" attribute, so
// every renderer below drops it before forwarding the rest.
const components: Components = {
  h1: ({ node, ...props }) => <Heading as="h1" size="sm" color="text.heading" mt={4} mb={2} {...props} />,
  h2: ({ node, ...props }) => <Heading as="h2" size="xs" color="text.heading" mt={4} mb={2} {...props} />,
  h3: ({ node, ...props }) => (
    <Heading as="h3" size="xs" color="text.subtle" textTransform="uppercase" letterSpacing="wide" mt={3} mb={1} {...props} />
  ),
  h4: ({ node, ...props }) => <Heading as="h4" size="xs" color="text.subtle" mt={3} mb={1} {...props} />,
  p: ({ node, ...props }) => <Text fontSize="sm" color="text.base" mb={2} lineHeight="tall" {...props} />,
  ul: ({ node, ...props }) => <UnorderedList fontSize="sm" color="text.base" pl={4} mb={2} spacing={1} {...props} />,
  ol: ({ node, ...props }) => <OrderedList fontSize="sm" color="text.base" pl={4} mb={2} spacing={1} {...props} />,
  li: ({ node, ...props }) => <ListItem lineHeight="tall" {...props} />,
  strong: ({ node, ...props }) => <Text as="strong" fontWeight="semibold" color="text.heading" {...props} />,
  em: ({ node, ...props }) => <Text as="em" {...props} />,
  hr: () => <Divider my={3} />,
  a: ({ node, ...props }) => <Link isExternal color="brand.base" textDecoration="underline" rel="noopener noreferrer" {...props} />,
  blockquote: ({ children }) => (
    <Box
      as="blockquote"
      borderLeftWidth="3px"
      borderColor="border.base"
      pl={3}
      py={1}
      my={2}
      color="text.subtle"
    >
      {children}
    </Box>
  ),
  // Inline code. Fenced blocks arrive wrapped in <pre>, which supplies the frame.
  code: ({ node, ...props }) => (
    <Code fontSize="xs" px={1} py={0} bg="background.container.subtle" color="text.base" {...props} />
  ),
  pre: ({ node, ...props }) => (
    <Box
      as="pre"
      fontFamily="mono"
      fontSize="xs"
      whiteSpace="pre-wrap"
      overflowX="auto"
      borderWidth="1px"
      borderColor="border.base"
      borderRadius="md"
      p={3}
      mb={2}
      bg="background.container.subtle"
      sx={{ code: { bg: 'transparent', px: 0, fontSize: 'xs' } }}
      {...props}
    />
  ),
  // Tables can be wider than the modal; scroll them rather than the page.
  table: ({ node, ...props }) => (
    <Box overflowX="auto" mb={2}>
      <Table size="sm" variant="simple" {...props} />
    </Box>
  ),
  thead: ({ node, ...props }) => <Thead bg="background.table.header" {...props} />,
  tbody: ({ node, ...props }) => <Tbody {...props} />,
  tr: ({ node, ...props }) => <Tr {...props} />,
  th: ({ node, ...props }) => <Th fontSize="xs" {...props} />,
  td: ({ node, ...props }) => <Td fontSize="xs" {...props} />,
};

export const CharterMarkdown: React.FC<{ text: string }> = ({ text }) => (
  <ReactMarkdown
    remarkPlugins={[remarkGfm]}
    // rehypePlugins is intentionally omitted — see note 2 above.
    disallowedElements={DISALLOWED}
    unwrapDisallowed
    components={components}
  >
    {text}
  </ReactMarkdown>
);

// CharterSource shows the charter exactly as stored, which is what the
// LLM-as-judge actually receives. Operators debugging a judge ruling need the
// literal bytes, not a prettified view.
export const CharterSource: React.FC<{ text: string }> = ({ text }) => (
  <Box
    as="pre"
    fontFamily="mono"
    fontSize="xs"
    whiteSpace="pre-wrap"
    overflowX="auto"
    borderWidth="1px"
    borderColor="border.base"
    borderRadius="md"
    p={3}
    bg="background.container.subtle"
    color="text.base"
  >
    {text}
  </Box>
);
