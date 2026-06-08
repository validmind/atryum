import React, { useCallback, useRef, useState } from 'react';
import { Box, Flex } from '@chakra-ui/react';

interface ResizablePanelsProps {
  left: React.ReactNode;
  right: React.ReactNode;
  initialSplit?: number;
  minLeft?: number;
  minRight?: number;
}

const ResizablePanels: React.FC<ResizablePanelsProps> = ({
  left,
  right,
  initialSplit = 0.4,
  minLeft = 240,
  minRight = 280,
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [splitFraction, setSplitFraction] = useState(initialSplit);
  const dragging = useRef(false);

  const onMouseDown = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      dragging.current = true;

      const onMouseMove = (mv: MouseEvent) => {
        if (!dragging.current || !containerRef.current) return;
        const rect = containerRef.current.getBoundingClientRect();
        const total = rect.width;
        const offset = mv.clientX - rect.left;
        const clamped = Math.min(Math.max(offset, minLeft), total - minRight);
        setSplitFraction(clamped / total);
      };

      const onMouseUp = () => {
        dragging.current = false;
        window.removeEventListener('mousemove', onMouseMove);
        window.removeEventListener('mouseup', onMouseUp);
      };

      window.addEventListener('mousemove', onMouseMove);
      window.addEventListener('mouseup', onMouseUp);
    },
    [minLeft, minRight],
  );

  return (
    <Flex ref={containerRef} h="full" overflow="hidden" position="relative">
      <Box
        flexShrink={0}
        style={{ width: `${splitFraction * 100}%` }}
        overflow="auto"
        h="full"
      >
        {left}
      </Box>

      <Box
        flexShrink={0}
        w="4px"
        cursor="col-resize"
        bg="border.base"
        _hover={{ bg: 'brand.base' }}
        transition="background 0.15s"
        onMouseDown={onMouseDown}
        zIndex={1}
      />

      <Box flex={1} overflow="auto" h="full" minW={0}>
        {right}
      </Box>
    </Flex>
  );
};

export default ResizablePanels;
