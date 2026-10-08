export type MemberWorkspaceNavigationProps = {
  items: readonly { id: string; label: string }[];
  activeTab: string;
  onSelect: (tab: string) => void;
};

export default function MemberWorkspaceNavigation({
  items,
  activeTab,
  onSelect,
}: MemberWorkspaceNavigationProps) {
  return (
    <nav aria-label="Member workspace">
      {items.map((item) => {
        const isActive = item.id === activeTab;
        return (
          <button
            className={`member-nav ${isActive ? 'active' : ''}`}
            type="button"
            onClick={() => onSelect(item.id)}
            data-nav-id={item.id}
            data-active={isActive}
            aria-current={isActive ? 'page' : undefined}
            key={item.id}
          >
            {item.label}
          </button>
        );
      })}
    </nav>
  );
}
