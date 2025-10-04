import { useState } from 'react';

export default function SearchBar() {
  const [query, setQuery] = useState('');

  const handleSearch = () => {
    alert(`Search for: ${query}`);
  };

  return (
    <div className="flex">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="px-2 py-1 rounded-l bg-gray-800 text-white focus:outline-none"
        placeholder="Search..."
      />
      <button
        onClick={handleSearch}
        className="px-3 py-1 bg-blue-500 rounded-r hover:bg-blue-600"
      >
        🔍
      </button>
    </div>
  );
}
