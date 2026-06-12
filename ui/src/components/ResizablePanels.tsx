import React, { useCallback, useRef, useState } from 'react';
import { Box, Flex } from '@chakra-ui/react';

const RIGHT_DEFAULT_WIDTH = 420;
const RIGHT_MAX_WIDTH = 800;

interface ResizablePanelsProps {
  left: React.ReactNode;
  right?: React.ReactNode;
  /** @deprecated – no longer used; right panel is a fixed overlay */
  initialSplit?: number;
  /** @deprecated – no longer used */
  minLeft?: number;
  minRight?: number;
}

const ResizablePanels: React.FC<ResizablePanelsProps> = ({
  left,
  right,
  minRight = 280,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [rightWidth, setRightWidth] = useState(RIGHT_DEFAULT_WIDTH);
  const isDragging = useRef(false);
  const startX = useRef(0);
  const startWidth = useRef(RIGHT_DEFAULT_WIDTH);

  const onMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!isDragging.current) return;
      // Dragging the left edge: moving left widens, moving right narrows
      const delta = startX.current - e.clientX;
      const next = Math.min(RIGHT_MAX_WIDTH, Math.max(minRight, startWidth.current + delta));
      setRightWidth(next);
    },
    [minRight],
  );

  const onMouseUp = useCallback(() => {
    if (!isDragging.current) return;
    isDragging.current = false;
    document.removeEventListener('mousemove', onMouseMove);
    document.removeEventListener('mouseup', onMouseUp);
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  }, [onMouseMove]);

  const onResizeHandleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      isDragging.current = true;
      startX.current = e.clientX;
      startWidth.current = rightWidth;
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';
      document.addEventListener('mousemove', onMouseMove);
      document.addEventListener('mouseup', onMouseUp);
    },
    [rightWidth, onMouseMove, onMouseUp],
  );

  const hasRight = right != null && right !== false;

  if (!hasRight) {
    return (
      <Flex ref={containerRef} h="full" overflow="hidden" position="relative">
        <Box flex={1} overflow="auto" h="full" minW={0}>
          {left}
        </Box>
      </Flex>
    );
  }

  return (
    <Flex ref={containerRef} h="full" overflow="hidden" position="relative">
      {/* Left panel fills all available space; right panel floats on top */}
      <Box flex={1} overflow="auto" h="full" minW={0}>
        {left}
      </Box>

      {/* Floating right panel — fixed, full viewport height, overlays content */}
      <Box
        position="fixed"
        top={0}
        right={0}
        h="100vh"
        w={`${rightWidth}px`}
        zIndex={50}
        bg="background.page"
        borderLeftWidth={1}
        borderColor="border.base"
        boxShadow="-4px 0 16px rgba(0,0,0,0.10)"
        overflow="hidden"
        display="flex"
        flexDirection="column"
      >
        {/* Resize handle on the left edge */}
        <Box
          position="absolute"
          top={0}
          left={0}
          w="5px"
          h="100%"
          cursor="col-resize"
          zIndex={51}
          onMouseDown={onResizeHandleMouseDown}
          _hover={{ bg: 'brand.base', opacity: 0.5 }}
          transition="background 0.15s, opacity 0.15s"
        />

        <Box flex={1} overflow="auto" h="full">
          {right}
        </Box>
      </Box>
    </Flex>
  );
};

export default ResizablePanels;
