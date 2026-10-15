import React from "react";
import {
  Box,
  Button,
  Flex,
  HStack,
  Heading,
  Icon,
  Image,
  Link,
  Stack,
  Text,
  VStack,
} from "@chakra-ui/react";
import { Link as RouterLink, useLocation } from "react-router-dom";
import type { ComponentType } from "react";

import {
  ArrowRightStartOnRectangleIcon,
  CircleStackIcon,
  ClipboardDocumentListIcon,
  Cog6ToothIcon,
  CpuChipIcon,
  QueueListIcon,
  ShieldCheckIcon,
  UsersIcon,
} from "@heroicons/react/24/outline";

import { useAdminAuth } from "../auth/adminAuth";
import { useMe } from "../hooks/useIdentity";
import atryumLogo from "../assets/atryum-logo.svg";

type NavItem = {
  label: string;
  icon: ComponentType;
  path: string;
  /** Hidden from members; the open-core operator API answers 403 for them. */
  adminOnly?: boolean;
};

const NAV_ITEMS: NavItem[] = [
  { label: "Invocations", icon: QueueListIcon, path: "/invocations", adminOnly: true },
  { label: "Plans", icon: ClipboardDocumentListIcon, path: "/plans", adminOnly: true },
  { label: "Agents", icon: CpuChipIcon, path: "/agents" },
  { label: "Servers", icon: CircleStackIcon, path: "/servers", adminOnly: true },
  { label: "Rules", icon: ShieldCheckIcon, path: "/rules", adminOnly: true },
  { label: "Users", icon: UsersIcon, path: "/users", adminOnly: true },
  { label: "Settings", icon: Cog6ToothIcon, path: "/settings", adminOnly: true },
];

/**
 * Nav items visible to the current principal. Until /api/v1/me has answered we
 * show the full menu so an admin never sees the sidebar collapse and re-expand;
 * once a member is confirmed, admin-only entries disappear.
 */
export const visibleNavItems = (role: string | undefined): NavItem[] =>
  role === "member" ? NAV_ITEMS.filter((item) => !item.adminOnly) : NAV_ITEMS;

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
    _hover={{ textDecoration: "none" }}
    _focus={{ textDecoration: "none" }}>
    <Flex
      pt={3}
      pb={3}
      pl={6}
      transition="background 0.2s"
      boxShadow={
        isActive
          ? "inset 4px 0px 0px 0px var(--chakra-colors-blue-500)"
          : "none"
      }
      bg="transparent"
      color={
        isActive
          ? "component.sidebar.main.menuitem.selected.text"
          : "component.sidebar.main.menuitem.up.text"
      }
      _hover={{
        bg: "component.sidebar.main.menuitem.hover.background",
        color: isActive
          ? "component.sidebar.main.menuitem.selected.text"
          : "component.sidebar.main.menuitem.hover.text",
      }}>
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
  const { status: authStatus, signOut } = useAdminAuth();
  const { data: me } = useMe();
  const navItems = visibleNavItems(me?.role);
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
        flexDirection="column">
        <VStack gap={4} alignItems="stretch" flex={1}>
          <Stack ml={6} mr={2} mb={2} mt={6} alignItems="stretch">
            <Link
              as={RouterLink}
              to="/invocations"
              _hover={{ textDecoration: "none" }}>
              <Image
                src={atryumLogo}
                alt="Atryum"
                objectFit="contain"
                h="45px"
                fallback={
                  <Heading size="md" color="blue.600">
                    Atryum
                  </Heading>
                }
              />
            </Link>
          </Stack>
          <Stack gap={0}>
            {navItems.map((item) => (
              <NavItemRow
                key={item.path}
                {...item}
                isActive={isActive(item.path)}
              />
            ))}
          </Stack>
        </VStack>
        {authStatus === "authenticated" ? (
          <Box borderTopWidth={1} borderColor="border.base" p={4}>
            <Button
              variant="ghost"
              w="full"
              justifyContent="flex-start"
              leftIcon={<Icon as={ArrowRightStartOnRectangleIcon} boxSize={6} />}
              onClick={() => void signOut()}>
              Log out
            </Button>
          </Box>
        ) : null}
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
