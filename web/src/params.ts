import { defineParams } from '@sveltejs/kit/params';

export const params = defineParams({
  dashboard: (section) => section === 'usage' || section === 'clients' ? section : undefined
});
