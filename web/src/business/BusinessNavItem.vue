<script setup lang="ts">
import UiIcon from '@/components/UiIcon.vue'

defineProps<{ active: boolean }>()
defineEmits<{ select: [] }>()
</script>

<template>
  <button
    type="button"
    :aria-current="active ? 'page' : undefined"
    :class="[
      'flex w-auto shrink-0 items-center gap-2 rounded-md px-3 py-2 text-left whitespace-nowrap transition md:w-full',
      active ? 'bg-blue-600 text-white' : 'text-gray-400 hover:bg-raised hover:text-white',
    ]"
    @click="$emit('select')"
  >
    <UiIcon name="models" :size="16" class="shrink-0" />
    <span class="min-w-0 truncate md:whitespace-normal">Gemini Business</span>
  </button>
</template>
