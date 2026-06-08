import { extendTheme } from '@chakra-ui/react';

export const theme = extendTheme({
  semanticTokens: {
    colors: {
      'background.page': { default: 'white' },
      'background.container.subtle': { default: 'gray.50' },
      'background.table.header': { default: 'gray.50' },
      'background.table.row.selected': { default: 'blue.50' },
      'background.table.row.hover': { default: 'gray.50' },
      'background.secondary': { default: 'gray.50' },
      'text.heading': { default: 'gray.800' },
      'text.subtle': { default: 'gray.500' },
      'text.base': { default: 'gray.700' },
      'text.secondary': { default: 'gray.500' },
      'text.error': { default: 'red.500' },
      'border.base': { default: 'gray.200' },
      'brand.base': { default: 'blue.500' },
      'component.sidebar.main.background': { default: 'white' },
      'component.sidebar.main.menuitem.up.text': { default: 'gray.600' },
      'component.sidebar.main.menuitem.hover.background': {
        default: 'gray.50',
      },
      'component.sidebar.main.menuitem.hover.text': { default: 'gray.800' },
      'component.sidebar.main.menuitem.selected.text': { default: 'blue.600' },
    },
  },
  components: {
    Button: {
      variants: {
        primary: {
          bg: 'blue.500',
          color: 'white',
          _hover: { bg: 'blue.600' },
          _active: { bg: 'blue.700' },
          _disabled: { opacity: 0.6, cursor: 'not-allowed' },
        },
        outlineDanger: {
          border: '1px solid',
          borderColor: 'red.400',
          color: 'red.500',
          bg: 'transparent',
          _hover: { bg: 'red.50' },
          _active: { bg: 'red.100' },
        },
      },
    },
  },
});
