import React from 'react';
import {
  Box,
  Flex,
  HStack,
  Heading,
  Icon,
  Image,
  Link,
  Stack,
  Text,
  VStack,
} from '@chakra-ui/react';
import { Link as RouterLink, useLocation } from 'react-router-dom';
import type { ComponentType } from 'react';

import {
  CircleStackIcon,
  Cog6ToothIcon,
  CpuChipIcon,
  QueueListIcon,
  ShieldCheckIcon,
} from '@heroicons/react/24/outline';

type NavItem = {
  label: string;
  icon: ComponentType;
  path: string;
};

const NAV_ITEMS: NavItem[] = [
  { label: 'Invocations', icon: QueueListIcon, path: '/invocations' },
  { label: 'Agents', icon: CpuChipIcon, path: '/agents' },
  { label: 'Servers', icon: CircleStackIcon, path: '/servers' },
  { label: 'Rules', icon: ShieldCheckIcon, path: '/rules' },
  { label: 'Settings', icon: Cog6ToothIcon, path: '/settings' },
];

type NavItemRowProps = NavItem & { isActive: boolean };

const NavItemRow: React.FC<NavItemRowProps> = ({
  label,
  icon,
  path,
  isActive,
}) => (
  <Link
    as={RouterLink}
    to={path}
    textDecoration="none"
    _hover={{ textDecoration: 'none' }}
    _focus={{ textDecoration: 'none' }}
  >
    <Flex
      pt={3}
      pb={3}
      pl={6}
      transition="background 0.2s"
      boxShadow={isActive ? 'inset 4px 0px 0px 0px var(--chakra-colors-blue-500)' : 'none'}
      bg="transparent"
      color={
        isActive
          ? 'component.sidebar.main.menuitem.selected.text'
          : 'component.sidebar.main.menuitem.up.text'
      }
      _hover={{
        bg: 'component.sidebar.main.menuitem.hover.background',
        color: isActive
          ? 'component.sidebar.main.menuitem.selected.text'
          : 'component.sidebar.main.menuitem.hover.text',
      }}
    >
      <HStack gap={2.5} alignItems="flex-start">
        <Icon as={icon} boxSize={6} />
        <Text fontWeight="bold" pt={0.5}>
          {label}
        </Text>
      </HStack>
    </Flex>
  </Link>
);

type LayoutProps = {
  children: React.ReactNode;
};

const Layout: React.FC<LayoutProps> = ({ children }) => {
  const location = useLocation();
  const isActive = (path: string) => location.pathname.startsWith(path);

  return (
    <Flex h="100vh">
      {/* Sidebar */}
      <Box
        w="15%"
        maxW="240px"
        minW="180px"
        bg="component.sidebar.main.background"
        borderRightWidth={1}
        borderColor="border.base"
        position="fixed"
        h="full"
        display="flex"
        flexDirection="column"
      >
        <VStack gap={4} alignItems="stretch" flex={1}>
          <Stack ml={6} mr={2} mb={2} mt={6} alignItems="stretch">
            <Link
              as={RouterLink}
              to="/invocations"
              _hover={{ textDecoration: 'none' }}
            >
              <Image
                src="/ui/atryum-logo.png"
                alt="Atryum"
                objectFit="contain"
                h="36px"
                fallback={
                  <Heading size="md" color="blue.600">
                    Atryum
                  </Heading>
                }
              />
            </Link>
          </Stack>
          <Stack gap={0}>
            {NAV_ITEMS.map((item) => (
              <NavItemRow
                key={item.path}
                {...item}
                isActive={isActive(item.path)}
              />
            ))}
          </Stack>
        </VStack>
      </Box>

      {/* Main content */}
      <Box ml="15%" minW={0} flex={1} p={8} bg="background.page">
        {children}
      </Box>
    </Flex>
  );
};

/** Simple page-level heading used across all pages. */
export const ContentPageTitle: React.FC<{ children: React.ReactNode }> = ({
  children,
}) => (
  <Heading size="lg" color="text.heading">
    {children}
  </Heading>
);

export default Layout;
